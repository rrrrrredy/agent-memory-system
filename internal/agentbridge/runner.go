package agentbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	loadoutcontext "github.com/rrrrrredy/agent-memory-system/internal/loadout"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

func Run(ctx context.Context, store *ledger.Store, request RunRequest, options Options) (RunResult, error) {
	result := RunResult{SchemaVersion: RunResultSchema, Privacy: PrivacyLocalOnly}
	if ctx == nil || store == nil || strings.TrimSpace(options.CodexPath) == "" {
		return result, errors.New("context, store, and Codex executable are required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	normalized, err := normalizeRequest(request)
	if err != nil {
		return result, err
	}
	if report := store.Verify(); len(report.Issues) != 0 {
		return result, fmt.Errorf("evidence ledger verification failed: %s", strings.Join(report.Issues, "; "))
	}
	delivery, err := resolveDelivery(store, normalized, options.PortableRoot)
	if err != nil {
		return result, err
	}
	prompt := normalized.Prompt
	memoryReferences := deliveryMemoryReferences(delivery)
	if delivery != nil {
		prompt = delivery.Content +
			"\nThe following is the current task. Current task instructions override memory:\n" +
			normalized.Prompt
	}
	requestData, err := canonicalBytes(normalized)
	if err != nil {
		return result, err
	}
	requestBlob, err := store.PutBlob(bytes.NewReader(requestData))
	if err != nil {
		return result, err
	}
	promptBlob, err := store.PutBlob(strings.NewReader(prompt))
	if err != nil {
		return result, err
	}
	codexPath, err := exec.LookPath(options.CodexPath)
	if err != nil {
		return result, fmt.Errorf("resolve Codex executable: %w", err)
	}
	codexArtifact, err := bindArtifact(store, codexPath)
	if err != nil {
		return result, fmt.Errorf("bind Codex executable: %w", err)
	}
	runnerPath, err := os.Executable()
	if err != nil {
		return result, fmt.Errorf("resolve agentmem executable: %w", err)
	}
	runnerArtifact, err := bindArtifact(store, runnerPath)
	if err != nil {
		return result, fmt.Errorf("bind agentmem executable: %w", err)
	}
	startedAt := options.Now().UTC()
	randomID, err := ledger.NewEventID(startedAt)
	if err != nil {
		return result, err
	}
	executionID := "native-agent-execution-" + randomID
	runtimeRoot := filepath.Join(store.Root(), "state", "native-agent-runtime", executionID)
	if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
		return result, fmt.Errorf("create native Agent runtime: %w", err)
	}
	defer os.RemoveAll(runtimeRoot)
	stagedCodex := filepath.Join(runtimeRoot, "codex"+filepath.Ext(codexPath))
	if err := stageExecutable(store, stagedCodex, codexArtifact); err != nil {
		return result, fmt.Errorf("stage Codex executable: %w", err)
	}
	version, err := commandOutput(ctx, 30*time.Second, stagedCodex, "--version")
	if err != nil || strings.TrimSpace(version) == "" {
		return result, errors.New("Codex executable version probe failed")
	}
	arguments := buildArguments(normalized)
	names := environmentNames()
	started := Started{
		SchemaVersion:           StartedSchema,
		ExecutionID:             executionID,
		TaskID:                  normalized.TaskID,
		RequestSHA256:           sha256Hex(requestData),
		RequestBlob:             requestBlob,
		PromptSHA256:            promptBlob.SHA256,
		PromptBlob:              promptBlob,
		CodexExecutable:         codexArtifact,
		CodexVersion:            strings.TrimSpace(version),
		RunnerExecutable:        runnerArtifact,
		Arguments:               arguments,
		EnvironmentPolicy:       "inherited_for_auth; names hashed; values intentionally not recorded",
		EnvironmentNamesSHA256:  environmentNamesSHA256(names),
		EnvironmentNamesCount:   len(names),
		WorkingDirectorySHA256:  sha256Hex([]byte(normalized.WorkingDirectory)),
		LoadoutContextReceiptID: normalized.LoadoutContextReceiptID,
		MemoryReferences:        memoryReferences,
		StartedAt:               startedAt,
		Privacy:                 PrivacyLocalOnly,
	}
	started.ArgumentsSHA256, err = sha256JSON(arguments)
	if err != nil {
		return result, err
	}
	started.StartedEventID, err = startedIdentity(started)
	if err != nil {
		return result, err
	}
	startedData, err := canonicalBytes(started)
	if err != nil {
		return result, err
	}
	startedBlob, err := store.PutBlob(bytes.NewReader(startedData))
	if err != nil {
		return result, err
	}
	parents := []string{}
	if normalized.LoadoutContextReceiptID != "" {
		parents = append(parents, normalized.LoadoutContextReceiptID)
	}
	startedRecord, err := appendEvent(store, started.StartedEventID, ledger.KindSystemEvent,
		executionID, startedBlob, StartedMediaType, parents, nil, startedAt)
	if err != nil {
		return result, err
	}
	if current, hashErr := hashFile(stagedCodex); hashErr != nil ||
		current.SHA256 != codexArtifact.SHA256 || current.Bytes != codexArtifact.Bytes {
		return result, errors.New("staged Codex executable changed before task execution")
	}

	runContext, cancel := context.WithTimeout(ctx, time.Duration(normalized.TimeoutSeconds)*time.Second)
	defer cancel()
	command := exec.CommandContext(runContext, stagedCodex, arguments...)
	command.Dir = normalized.WorkingDirectory
	command.Env = os.Environ()
	command.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	runErr := command.Run()
	exitCode := commandExitCode(runErr, runContext.Err())
	rawEventsBlob, putErr := store.PutBlob(bytes.NewReader(stdout.Bytes()))
	if putErr != nil {
		return result, putErr
	}
	var stderrBlob *ledger.BlobRef
	if stderr.Len() != 0 {
		reference, blobErr := store.PutBlob(bytes.NewReader(stderr.Bytes()))
		if blobErr != nil {
			return result, blobErr
		}
		stderrBlob = &reference
	}
	parsed, parseErr := parseExecutionJSONL(stdout.Bytes())
	var messageBlob *ledger.BlobRef
	if parseErr == nil {
		reference, blobErr := store.PutBlob(strings.NewReader(parsed.AgentMessage))
		if blobErr != nil {
			return result, blobErr
		}
		messageBlob = &reference
	}
	failureKind := ""
	switch {
	case runContext.Err() != nil:
		failureKind = "timeout"
	case runErr != nil:
		failureKind = "process_exit"
	case parseErr != nil:
		failureKind = "invalid_jsonl"
	}
	if current, hashErr := hashFile(stagedCodex); hashErr != nil ||
		current.SHA256 != codexArtifact.SHA256 || current.Bytes != codexArtifact.Bytes {
		failureKind = "executable_changed"
	}
	outcome := OutcomeCompleted
	if failureKind != "" {
		outcome = OutcomeFailed
	}
	finishedAt := options.Now().UTC()
	receipt := Receipt{
		SchemaVersion:       ReceiptSchema,
		ExecutionID:         executionID,
		TaskID:              normalized.TaskID,
		Started:             BoundEvent{EventID: started.StartedEventID, RecordSHA256: startedRecord.RecordHash},
		RawEventsBlob:       rawEventsBlob,
		AgentMessageBlob:    messageBlob,
		StderrBlob:          stderrBlob,
		ThreadID:            parsed.ThreadID,
		Events:              parsed.Events,
		ToolCalls:           parsed.ToolCalls,
		Usage:               parsed.Usage,
		ProcessExitCode:     exitCode,
		Outcome:             outcome,
		FailureKind:         failureKind,
		ReasoningVisibility: parsed.ReasoningVisibility,
		ProviderAuthority:   ProviderAuthority,
		StartedAt:           startedAt,
		FinishedAt:          finishedAt,
		Privacy:             PrivacyLocalOnly,
	}
	receipt.ReceiptID, err = receiptIdentity(receipt)
	if err != nil {
		return result, err
	}
	receiptData, err := canonicalBytes(receipt)
	if err != nil {
		return result, err
	}
	receiptBlob, err := store.PutBlob(bytes.NewReader(receiptData))
	if err != nil {
		return result, err
	}
	threadID := parsed.ThreadID
	if threadID == "" {
		threadID = executionID
	}
	reasoning := &ledger.ReasoningCapture{
		Visibility: parsed.ReasoningVisibility,
		Provider:   "codex-cli",
		Model:      normalized.Model,
		Note:       "Visibility describes only reasoning content exposed by Codex exec JSONL; provider-private chain-of-thought is not captured.",
	}
	if _, err := appendEvent(store, receipt.ReceiptID, ledger.KindToolResult, threadID,
		receiptBlob, ReceiptMediaType, []string{started.StartedEventID}, reasoning, finishedAt); err != nil {
		return result, err
	}
	result.Receipt = receipt
	if receipt.Outcome == OutcomeFailed {
		return result, &ExecutionError{ReceiptID: receipt.ReceiptID, FailureKind: receipt.FailureKind}
	}
	return result, nil
}

func normalizeRequest(request RunRequest) (RunRequest, error) {
	if err := validateRequest(request); err != nil {
		return RunRequest{}, err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(request.WorkingDirectory))
	if err != nil {
		return RunRequest{}, fmt.Errorf("resolve native Agent working directory: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return RunRequest{}, errors.New("native Agent working directory is not a directory")
	}
	request.WorkingDirectory = resolved
	return request, nil
}

func resolveDelivery(store *ledger.Store, request RunRequest, portableRoot string) (*loadoutcontext.ContextReceipt, error) {
	if request.LoadoutContextReceiptID == "" {
		if strings.TrimSpace(portableRoot) != "" {
			return nil, errors.New("portable repository is only accepted with a loadout context receipt")
		}
		return nil, nil
	}
	if strings.TrimSpace(portableRoot) == "" {
		return nil, errors.New("loadout-backed native Agent execution requires a portable repository")
	}
	receipt, err := loadoutcontext.ResolveVerifiedContext(store, request.LoadoutContextReceiptID)
	if err != nil {
		return nil, err
	}
	if receipt.Context.Agent != ledger.AgentCodex {
		return nil, errors.New("loadout context receipt was not issued for Codex")
	}
	if receipt.Context.Task != "" && receipt.Context.Task != request.TaskID {
		return nil, errors.New("loadout context task scope does not match the native Agent task")
	}
	current, report, err := portable.LoadCurrentLoadout(portableRoot, receipt.Loadout.LoadoutID)
	if err != nil {
		if len(report.Issues) != 0 {
			return nil, fmt.Errorf("portable repository verification failed: %s", report.Issues[0].Message)
		}
		return nil, err
	}
	if !reflect.DeepEqual(current, receipt.Loadout) {
		return nil, errors.New("loadout context no longer matches the current promoted memory heads")
	}
	return &receipt, nil
}

func deliveryMemoryReferences(receipt *loadoutcontext.ContextReceipt) []retrieval.MemoryReference {
	if receipt == nil {
		return []retrieval.MemoryReference{}
	}
	return append([]retrieval.MemoryReference(nil), receipt.Memories...)
}

func appendEvent(store *ledger.Store, eventID string, kind ledger.EventKind, threadID string,
	blob ledger.BlobRef, mediaType string, parents []string, reasoning *ledger.ReasoningCapture,
	at time.Time) (ledger.Record, error) {
	payload := ledger.Payload{
		Encoding: "binary", MediaType: mediaType, Blob: &blob,
		SHA256: blob.SHA256, Bytes: blob.Bytes,
	}
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion,
		EventID:       eventID,
		Kind:          kind,
		ObservedAt:    at,
		RecordedAt:    at,
		Source: ledger.Source{
			Agent:          ledger.AgentCodex,
			Adapter:        "agentmem-native-agent",
			AdapterVersion: AdapterVersion,
			DeviceID:       store.DeviceID(),
			OS:             runtime.GOOS,
			ThreadID:       threadID,
			SessionID:      threadID,
			SourceEventID:  eventID,
			SourceCursor:   eventID,
		},
		Payload:      &payload,
		Reasoning:    reasoning,
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy:      ledger.Privacy{Classification: PrivacyLocalOnly},
	}
	if len(parents) != 0 {
		event.Causality = &ledger.Causality{ParentEventIDs: append([]string(nil), parents...)}
	}
	return store.Append(event)
}

func commandOutput(ctx context.Context, timeout time.Duration, path string, arguments ...string) (string, error) {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := exec.CommandContext(commandContext, path, arguments...).CombinedOutput()
	return string(output), err
}

func commandExitCode(err error, contextErr error) int {
	if contextErr != nil {
		return 124
	}
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return 126
}
