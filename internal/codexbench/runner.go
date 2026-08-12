package codexbench

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

const (
	maximumTasks         = 50
	maximumArtifactFiles = 4096
	maximumArtifactBytes = 256 << 20
)

type resolvedTask struct {
	task       Task
	oraclePath string
	sealed     SealedTask
}

const minimumDistinctTaskClusters = 20

func Run(ctx context.Context, store *ledger.Store, options Options) (Report, error) {
	result := Report{SchemaVersion: ReportSchema, Pairs: []PairResult{}, Issues: []string{}, Privacy: "local_only"}
	if ctx == nil || store == nil || strings.TrimSpace(options.PlanPath) == "" ||
		strings.TrimSpace(options.CodexPath) == "" || strings.TrimSpace(options.OutputRoot) == "" {
		return result, errors.New("context, store, plan, Codex executable, and output root are required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	planData, err := os.ReadFile(options.PlanPath)
	if err != nil {
		return result, fmt.Errorf("read benchmark plan: %w", err)
	}
	var plan Plan
	if err := decodeStrict(planData, &plan); err != nil || validatePlan(plan) != nil {
		return result, errors.New("Codex benchmark plan is invalid")
	}
	planDirectory, err := filepath.Abs(filepath.Dir(options.PlanPath))
	if err != nil {
		return result, err
	}
	if err := createOutputRoot(options.OutputRoot); err != nil {
		return result, err
	}
	inputPlanBlob, err := store.PutBlob(bytes.NewReader(planData))
	if err != nil {
		return result, err
	}
	codexSourcePath, err := exec.LookPath(options.CodexPath)
	if err != nil {
		return result, fmt.Errorf("resolve Codex executable: %w", err)
	}
	codexSourcePath, err = filepath.Abs(codexSourcePath)
	if err != nil {
		return result, err
	}
	codexArtifact, err := executableArtifact(codexSourcePath)
	if err != nil {
		return result, err
	}
	codexArtifact, err = bindArtifactBlob(store, codexSourcePath, codexArtifact)
	if err != nil {
		return result, err
	}
	codexPath := filepath.Join(options.OutputRoot, "runtime", "codex"+filepath.Ext(codexSourcePath))
	if err := stageExecutable(store, codexPath, codexArtifact); err != nil {
		return result, fmt.Errorf("stage Codex executable: %w", err)
	}
	version, err := commandOutput(ctx, 30*time.Second, codexPath, "--version")
	codexArtifact.Path = filepath.ToSlash(filepath.Join("runtime", filepath.Base(codexPath)))
	if err != nil || strings.TrimSpace(version) == "" {
		return result, errors.New("Codex executable version probe failed")
	}
	runnerPath, err := os.Executable()
	if err != nil {
		return result, fmt.Errorf("resolve benchmark runner executable: %w", err)
	}
	runnerArtifact, err := executableArtifact(runnerPath)
	if err != nil {
		return result, fmt.Errorf("hash benchmark runner executable: %w", err)
	}
	runnerArtifact, err = bindArtifactBlob(store, runnerPath, runnerArtifact)
	if err != nil {
		return result, fmt.Errorf("bind benchmark runner executable: %w", err)
	}
	runnerArtifact.Path = filepath.Base(runnerPath)
	resolved, err := resolveTasks(store, planDirectory, options.OutputRoot, plan)
	if err != nil {
		return result, err
	}
	sealed := SealedPlan{SchemaVersion: SealedPlanSchema, SuiteID: plan.SuiteID,
		Model: plan.Model, TimeoutSeconds: plan.TimeoutSeconds, CodexExecutable: codexArtifact,
		CodexVersion: strings.TrimSpace(version), RunnerVersion: RunnerVersion,
		RunnerExecutable:       runnerArtifact,
		EnvironmentPolicy:      "inherited_for_auth; names hashed; values intentionally not recorded",
		EnvironmentNamesSHA256: environmentNamesSHA256(), EnvironmentNamesCount: len(environmentNames()),
		InputPlanBlob: inputPlanBlob, CreatedAt: options.Now().UTC(),
		Tasks: []SealedTask{}, Privacy: "local_only"}
	for _, item := range resolved {
		sealed.Tasks = append(sealed.Tasks, item.sealed)
	}
	sealed.PlanSHA256, err = sealedPlanSHA256(sealed)
	if err != nil {
		return result, err
	}
	sealedData, err := marshalIndented(sealed)
	if err != nil {
		return result, err
	}
	if err := writeExclusive(filepath.Join(options.OutputRoot, "sealed-plan.json"), sealedData); err != nil {
		return result, err
	}
	planBlob, err := store.PutBlob(bytes.NewReader(sealedData))
	if err != nil {
		return result, err
	}
	planEventID := "codex-benchmark-plan-" + sealed.PlanSHA256
	planRecord, err := appendBenchmarkEvent(store, planEventID, ledger.KindSystemEvent,
		ledger.AgentUnknown, plan.SuiteID, planBlob, "application/json", nil, options.Now().UTC())
	if err != nil {
		return result, err
	}

	result.PlanSHA256, result.PlanBlob, result.InputPlanBlob, result.PlanEventID =
		sealed.PlanSHA256, planBlob, inputPlanBlob, planEventID
	result.SuiteID, result.StartedAt = plan.SuiteID, options.Now().UTC()
	for index := range resolved {
		pair := PairResult{TaskID: resolved[index].task.TaskID, ClusterID: resolved[index].task.ClusterID,
			MemorySource: resolved[index].sealed.MemorySource,
			ToolPolicy:   resolved[index].sealed.ToolPolicy}
		for order, condition := range resolved[index].sealed.ExecutionOrder {
			arm, runErr := runArm(ctx, store, options, sealed, resolved[index], condition, order,
				codexPath, planRecord.Event.EventID)
			if condition == "baseline" {
				pair.Baseline = arm
			} else {
				pair.Treatment = arm
			}
			if runErr != nil {
				result.Issues = append(result.Issues, fmt.Sprintf("task %s %s: %v", pair.TaskID, condition, runErr))
				if arm.ResultEventID == "" {
					return result, fmt.Errorf("task %s %s failed before terminal evidence: %w", pair.TaskID, condition, runErr)
				}
			}
		}
		pair.TokenDelta = totalTokens(pair.Treatment.Usage) - totalTokens(pair.Baseline.Usage)
		switch {
		case pair.Treatment.OraclePassed && !pair.Baseline.OraclePassed:
			pair.Outcome = "win"
		case !pair.Treatment.OraclePassed && pair.Baseline.OraclePassed:
			pair.Outcome = "loss"
		default:
			pair.Outcome = "tie"
		}
		result.Pairs = append(result.Pairs, pair)
	}
	result.FinishedAt = options.Now().UTC()
	classifyReport(&result)
	result.ReportSHA256, err = reportSHA256(result)
	if err != nil {
		return result, err
	}
	reportData, err := marshalIndented(result)
	if err != nil {
		return result, err
	}
	if err := writeExclusive(filepath.Join(options.OutputRoot, "report.json"), reportData); err != nil {
		return result, err
	}
	reportBlob, err := store.PutBlob(bytes.NewReader(reportData))
	if err != nil {
		return result, err
	}
	parents := []string{planEventID}
	for _, pair := range result.Pairs {
		parents = append(parents, pair.Baseline.ResultEventID, pair.Treatment.ResultEventID)
	}
	_, err = appendBenchmarkEvent(store, "codex-benchmark-report-"+result.ReportSHA256,
		ledger.KindSystemEvent, ledger.AgentUnknown, plan.SuiteID, reportBlob, ReportMediaType,
		parents, result.FinishedAt)
	return result, err
}

func runArm(ctx context.Context, store *ledger.Store, options Options, plan SealedPlan,
	resolved resolvedTask, condition string, order int, codexPath, planEventID string) (ArmResult, error) {
	result := ArmResult{TaskID: resolved.task.TaskID, Condition: condition, ExecutionOrder: order,
		Issues: []string{}, StartedAt: options.Now().UTC()}
	if condition != "baseline" && condition != "memory" {
		return result, errors.New("execution condition is invalid")
	}
	workspace := filepath.Join(options.OutputRoot, "workspaces", resolved.task.TaskID, condition)
	if err := copyArtifacts(store, workspace, resolved.sealed.WorkspaceFiles); err != nil {
		return result, err
	}
	if err := initializeGitWorkspace(ctx, workspace); err != nil {
		return result, err
	}
	before, _, err := treeArtifacts(workspace)
	if err != nil {
		return result, err
	}
	result.WorkspaceBeforeSHA256 = artifactSetSHA256(before)
	prompt := resolved.task.Prompt
	if condition == "memory" {
		prompt += "\n\nReviewed project memory follows. Apply it only when relevant to this task:\n<project_memory>\n" +
			resolved.task.MemoryContext + "\n</project_memory>"
	}
	if resolved.task.ToolPolicy == "forbid" {
		prompt += "\n\nDo not call tools, inspect files, search the filesystem, or use external sources. " +
			"Answer only from the task prompt and any project memory supplied above."
	}
	startedPayload, _ := json.Marshal(map[string]any{"schema_version": "codex-benchmark-arm-start/v1alpha1",
		"plan_sha256": plan.PlanSHA256, "task_id": result.TaskID, "condition": condition,
		"prompt_sha256": sha256Hex([]byte(prompt)), "workspace_sha256": result.WorkspaceBeforeSHA256,
		"codex_sha256": plan.CodexExecutable.SHA256, "model": plan.Model, "tool_policy": resolved.task.ToolPolicy})
	startedBlob, err := store.PutBlob(bytes.NewReader(startedPayload))
	if err != nil {
		return result, err
	}
	result.StartedEventID = "codex-benchmark-start-" + sha256Hex(startedPayload)
	if _, err := appendBenchmarkEvent(store, result.StartedEventID, ledger.KindSystemEvent,
		ledger.AgentCodex, result.TaskID+"-"+condition, startedBlob, "application/json",
		[]string{planEventID}, result.StartedAt); err != nil {
		return result, err
	}
	if current, hashErr := executableArtifact(codexPath); hashErr != nil || current.SHA256 != plan.CodexExecutable.SHA256 {
		return result, errors.New("Codex executable changed after plan sealing")
	}
	args := []string{"exec", "--json", "--ephemeral", "--ignore-user-config", "--ignore-rules",
		"--sandbox", "workspace-write", "--cd", workspace}
	if plan.Model != "default" {
		args = append(args, "--model", plan.Model)
	}
	args = append(args, prompt)
	runContext, cancel := context.WithTimeout(ctx, time.Duration(plan.TimeoutSeconds)*time.Second)
	defer cancel()
	command := exec.CommandContext(runContext, codexPath, args...)
	command.Dir, command.Env = workspace, os.Environ()
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	runErr := command.Run()
	result.CodexExitCode = commandExitCode(runErr, runContext.Err())
	rawBlob, putErr := store.PutBlob(bytes.NewReader(stdout.Bytes()))
	if putErr != nil {
		return result, putErr
	}
	result.RawEventsBlob = rawBlob
	if stderr.Len() > 0 {
		blob, err := store.PutBlob(bytes.NewReader(stderr.Bytes()))
		if err != nil {
			return result, err
		}
		result.StderrBlob = &blob
	}
	parsed, parseErr := parseExecutionJSONL(stdout.Bytes())
	if parseErr != nil {
		result.Issues = append(result.Issues, parseErr.Error())
	} else {
		result.ThreadID, result.Usage, result.ToolCalls = parsed.ThreadID, parsed.Usage, parsed.ToolCalls
		result.AgentMessageSHA256 = sha256Hex([]byte(parsed.AgentMessage))
	}
	if resolved.task.ToolPolicy == "forbid" && result.ToolCalls != 0 {
		result.Issues = append(result.Issues,
			fmt.Sprintf("sealed no-tools task emitted %d tool calls", result.ToolCalls))
	}
	if runErr != nil {
		result.Issues = append(result.Issues, fmt.Sprintf("Codex exit code %d", result.CodexExitCode))
	}
	after, _, err := treeArtifacts(workspace)
	if err != nil {
		return result, err
	}
	result.WorkspaceAfterSHA256 = artifactSetSHA256(after)
	if parseErr == nil {
		messageBlob, err := store.PutBlob(strings.NewReader(parsed.AgentMessage))
		if err != nil {
			return result, err
		}
		result.AgentMessageBlob = &messageBlob
		resultDirectory := filepath.Join(workspace, ".agentmem-eval")
		if err := os.Mkdir(resultDirectory, 0o700); err != nil {
			return result, errors.New("benchmark workspace reserves .agentmem-eval for exact result artifacts")
		}
		if err := os.WriteFile(filepath.Join(resultDirectory, "agent-message.txt"), []byte(parsed.AgentMessage), 0o600); err != nil {
			return result, err
		}
	}
	if err := copyArtifacts(store, workspace, resolved.sealed.OracleFiles); err != nil {
		return result, err
	}
	if current, hashErr := executableArtifact(resolved.oraclePath); hashErr != nil || current.SHA256 != resolved.sealed.OracleExecutable.SHA256 {
		return result, errors.New("oracle executable changed after plan sealing")
	}
	oracleContext, oracleCancel := context.WithTimeout(ctx, time.Duration(plan.TimeoutSeconds)*time.Second)
	defer oracleCancel()
	oracle := exec.CommandContext(oracleContext, resolved.oraclePath, resolved.sealed.OracleArguments...)
	oracle.Dir, oracle.Env = workspace, os.Environ()
	var oracleOutput bytes.Buffer
	oracle.Stdout, oracle.Stderr = &oracleOutput, &oracleOutput
	oracleErr := oracle.Run()
	result.OracleExitCode = commandExitCode(oracleErr, oracleContext.Err())
	toolPolicyPassed := resolved.task.ToolPolicy != "forbid" || result.ToolCalls == 0
	result.OraclePassed = runErr == nil && parseErr == nil && oracleErr == nil && toolPolicyPassed
	oracleBlob, err := store.PutBlob(bytes.NewReader(oracleOutput.Bytes()))
	if err != nil {
		return result, err
	}
	result.OracleOutputBlob = oracleBlob
	result.FinishedAt = options.Now().UTC()
	resultPayload, _ := json.Marshal(result)
	resultBlob, err := store.PutBlob(bytes.NewReader(resultPayload))
	if err != nil {
		return result, err
	}
	result.ResultEventID = "codex-benchmark-result-" + sha256Hex(resultPayload)
	_, err = appendBenchmarkEvent(store, result.ResultEventID, ledger.KindToolResult,
		ledger.AgentCodex, result.TaskID+"-"+condition, resultBlob, "application/json",
		[]string{result.StartedEventID}, result.FinishedAt)
	if err != nil {
		return result, err
	}
	if len(result.Issues) != 0 {
		return result, errors.New(strings.Join(result.Issues, "; "))
	}
	return result, nil
}

func validatePlan(plan Plan) error {
	if plan.SchemaVersion != PlanSchema || strings.TrimSpace(plan.SuiteID) == "" ||
		strings.TrimSpace(plan.Model) == "" || plan.TimeoutSeconds < 30 || plan.TimeoutSeconds > 3600 ||
		len(plan.Tasks) == 0 || len(plan.Tasks) > maximumTasks || plan.Privacy != "local_only" {
		return errors.New("plan envelope is invalid")
	}
	seen := map[string]struct{}{}
	for _, task := range plan.Tasks {
		hasInlineMemory := strings.TrimSpace(task.MemoryContext) != ""
		hasInjection := strings.TrimSpace(task.InjectionID) != ""
		if !safeID(task.TaskID) || !safeID(task.ClusterID) || strings.TrimSpace(task.Prompt) == "" || len(task.Prompt) > 64*1024 ||
			(task.ToolPolicy != "forbid" && task.ToolPolicy != "allow") || hasInlineMemory == hasInjection ||
			len(task.MemoryContext) > 64*1024 || len(task.InjectionID) > 512 ||
			!safeRelative(task.Workspace) || !safeRelative(task.OracleOverlay) || len(task.OracleCommand) == 0 {
			return errors.New("plan task is invalid")
		}
		if _, duplicate := seen[task.TaskID]; duplicate {
			return errors.New("plan repeats a task id")
		}
		for _, argument := range task.OracleCommand[1:] {
			if !safeOracleArgument(argument) {
				return errors.New("oracle argument may not reference an unsealed path")
			}
		}
		seen[task.TaskID] = struct{}{}
	}
	return nil
}

func resolveTasks(store *ledger.Store, base, outputRoot string, plan Plan) ([]resolvedTask, error) {
	result := make([]resolvedTask, 0, len(plan.Tasks))
	for _, task := range plan.Tasks {
		memorySource := "caller_provided"
		memoryReferences := []retrieval.MemoryReference{}
		retrievalReceiptID := ""
		if task.InjectionID != "" {
			receipt, err := retrieval.ResolveVerifiedInjection(store, task.InjectionID)
			if err != nil {
				return nil, fmt.Errorf("task %s verified injection: %w", task.TaskID, err)
			}
			task.MemoryContext = receipt.Content
			memorySource = "verified_injection"
			retrievalReceiptID = receipt.RetrievalReceiptID
			memoryReferences = append(memoryReferences, receipt.Memories...)
		}
		workspace, err := resolveUnder(base, task.Workspace)
		if err != nil {
			return nil, err
		}
		overlay, err := resolveUnder(base, task.OracleOverlay)
		if err != nil {
			return nil, err
		}
		workspaceFiles, _, err := treeArtifacts(workspace)
		if err != nil {
			return nil, fmt.Errorf("task %s workspace: %w", task.TaskID, err)
		}
		workspaceFiles, err = bindArtifactBlobs(store, workspace, workspaceFiles)
		if err != nil {
			return nil, fmt.Errorf("task %s bind workspace artifacts: %w", task.TaskID, err)
		}
		oracleFiles, _, err := treeArtifacts(overlay)
		if err != nil {
			return nil, fmt.Errorf("task %s oracle overlay: %w", task.TaskID, err)
		}
		oraclePath, err := exec.LookPath(task.OracleCommand[0])
		oracleFiles, err = bindArtifactBlobs(store, overlay, oracleFiles)
		if err != nil {
			return nil, fmt.Errorf("task %s bind oracle artifacts: %w", task.TaskID, err)
		}
		if err != nil {
			return nil, fmt.Errorf("task %s oracle executable: %w", task.TaskID, err)
		}
		oraclePath, _ = filepath.Abs(oraclePath)
		oracleArtifact, err := executableArtifact(oraclePath)
		if err != nil {
			return nil, err
		}
		order := []string{"baseline", "memory"}
		stagedOracle := filepath.Join(outputRoot, "runtime", "oracle-"+task.TaskID+filepath.Ext(oraclePath))
		oracleArtifact, err = bindArtifactBlob(store, oraclePath, oracleArtifact)
		if err != nil {
			return nil, fmt.Errorf("task %s bind oracle executable: %w", task.TaskID, err)
		}
		if err := stageExecutable(store, stagedOracle, oracleArtifact); err != nil {
			return nil, fmt.Errorf("task %s stage oracle executable: %w", task.TaskID, err)
		}
		oraclePath = stagedOracle
		oracleArtifact.Path = filepath.ToSlash(filepath.Join("runtime", filepath.Base(stagedOracle)))
		assignment := sha256.Sum256([]byte(strings.Join([]string{
			RunnerVersion, task.TaskID, task.ClusterID, sha256Hex([]byte(task.Prompt)), sha256Hex([]byte(task.MemoryContext)),
			task.ToolPolicy, artifactSetSHA256(workspaceFiles), artifactSetSHA256(oracleFiles), oracleArtifact.SHA256,
		}, "\x00")))
		if assignment[0]&1 == 1 {
			order[0], order[1] = order[1], order[0]
		}
		sealed := SealedTask{TaskID: task.TaskID, ClusterID: task.ClusterID, PromptSHA256: sha256Hex([]byte(task.Prompt)),
			MemorySHA256: sha256Hex([]byte(task.MemoryContext)), MemorySource: memorySource, InjectionID: task.InjectionID,
			RetrievalReceiptID: retrievalReceiptID, MemoryReferences: memoryReferences,
			ToolPolicy:      task.ToolPolicy,
			WorkspaceSHA256: artifactSetSHA256(workspaceFiles), WorkspaceFiles: workspaceFiles,
			OracleOverlaySHA256: artifactSetSHA256(oracleFiles), OracleFiles: oracleFiles,
			OracleExecutable: oracleArtifact, OracleArguments: append([]string{}, task.OracleCommand[1:]...),
			ExecutionOrder: order}
		result = append(result, resolvedTask{task: task, oraclePath: oraclePath, sealed: sealed})
	}
	return result, nil
}

func treeArtifacts(root string) ([]Artifact, int64, error) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, 0, errors.New("artifact root is unavailable")
	}
	items := []Artifact{}
	var total int64
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == ".git" || strings.HasPrefix(relative, ".git"+string(filepath.Separator)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return errors.New("artifact root contains Git metadata")
		}
		if relative == ".agentmem-eval" || strings.HasPrefix(filepath.ToSlash(relative), ".agentmem-eval/") {
			return errors.New("artifact tree contains the reserved benchmark result directory")
		}
		if relative == "." || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("artifact tree contains a non-regular file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		total += int64(len(data))
		if len(items) >= maximumArtifactFiles || total > maximumArtifactBytes {
			return errors.New("artifact tree exceeds the benchmark safety limit")
		}
		items = append(items, Artifact{Path: filepath.ToSlash(relative), SHA256: sha256Hex(data), Bytes: int64(len(data))})
		return nil
	})
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	return items, total, err
}

func copyArtifacts(store *ledger.Store, target string, items []Artifact) error {
	for _, item := range items {
		path := filepath.Join(target, filepath.FromSlash(item.Path))
		if err := stageArtifact(store, path, item, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func artifactSetSHA256(items []Artifact) string {
	type identity struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Bytes  int64  `json:"bytes"`
	}
	identities := make([]identity, 0, len(items))
	for _, item := range items {
		identities = append(identities, identity{Path: item.Path, SHA256: item.SHA256, Bytes: item.Bytes})
	}
	data, _ := json.Marshal(identities)
	return sha256Hex(data)
}

func executableArtifact(path string) (Artifact, error) {
	file, err := os.Open(path)
	if err != nil {
		return Artifact{}, err
	}
	defer file.Close()
	hasher := sha256.New()
	bytesRead, err := io.Copy(hasher, file)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Path: filepath.Base(path), SHA256: hex.EncodeToString(hasher.Sum(nil)), Bytes: bytesRead}, nil
}

func bindArtifactBlob(store *ledger.Store, path string, artifact Artifact) (Artifact, error) {
	file, err := os.Open(path)
	if err != nil {
		return Artifact{}, err
	}
	defer file.Close()
	blob, err := store.PutBlob(file)
	if err != nil {
		return Artifact{}, err
	}
	if blob.SHA256 != artifact.SHA256 || blob.Bytes != artifact.Bytes {
		return Artifact{}, errors.New("artifact changed while binding its evidence blob")
	}
	artifact.Blob = blob
	return artifact, nil
}

func bindArtifactBlobs(store *ledger.Store, root string, artifacts []Artifact) ([]Artifact, error) {
	result := make([]Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		bound, err := bindArtifactBlob(store, filepath.Join(root, filepath.FromSlash(artifact.Path)), artifact)
		if err != nil {
			return nil, err
		}
		result = append(result, bound)
	}
	return result, nil
}

func stageExecutable(store *ledger.Store, destination string, expected Artifact) error {
	return stageArtifact(store, destination, expected, 0o700)
}

func stageArtifact(store *ledger.Store, destination string, expected Artifact, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	input, err := store.OpenBlob(expected.Blob)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	actual, err := executableArtifact(destination)
	if err != nil || actual.SHA256 != expected.SHA256 || actual.Bytes != expected.Bytes ||
		expected.Blob.SHA256 != expected.SHA256 || expected.Blob.Bytes != expected.Bytes {
		return errors.New("staged executable does not match its sealed artifact")
	}
	return nil
}

func initializeGitWorkspace(ctx context.Context, root string) error {
	command := exec.CommandContext(ctx, "git", "init", "--initial-branch", "main")
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("initialize benchmark Git workspace: %w: %s", err, output)
	}
	return nil
}

func appendBenchmarkEvent(store *ledger.Store, eventID string, kind ledger.EventKind, agent ledger.Agent,
	thread string, blob ledger.BlobRef, mediaType string, parents []string, at time.Time) (ledger.Record, error) {
	payload := ledger.Payload{Encoding: "binary", MediaType: mediaType, Blob: &blob,
		SHA256: blob.SHA256, Bytes: blob.Bytes}
	event := ledger.Event{SchemaVersion: ledger.SchemaVersion, EventID: eventID, Kind: kind,
		ObservedAt: at, RecordedAt: at, Source: ledger.Source{Agent: agent, Adapter: "codex-benchmark",
			AdapterVersion: RunnerVersion, DeviceID: store.DeviceID(), OS: runtime.GOOS,
			ThreadID: thread, SessionID: thread, SourceEventID: eventID, SourceCursor: eventID},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"}}
	if len(parents) > 0 {
		sort.Strings(parents)
		event.Causality = &ledger.Causality{ParentEventIDs: parents}
	}
	return store.Append(event)
}

func summarize(pairs []PairResult) Summary {
	result := Summary{Pairs: len(pairs)}
	clusters := map[string]struct{}{}
	var tokens int
	for _, pair := range pairs {
		if pair.Baseline.OraclePassed {
			result.BaselinePasses++
		}
		if pair.Treatment.OraclePassed {
			result.TreatmentPasses++
		}
		if pair.ToolPolicy == "forbid" && pair.Baseline.ToolCalls == 0 && pair.Treatment.ToolCalls == 0 {
			result.ToolFreePairs++
		}
		if pair.MemorySource == "verified_injection" {
			result.VerifiedRetrievalPairs++
		}
		switch pair.Outcome {
		case "win":
			result.Wins++
		case "loss":
			result.Losses++
		default:
			result.Ties++
		}
		tokens += pair.TokenDelta
		clusters[pair.ClusterID] = struct{}{}
	}
	result.DistinctClusters = len(clusters)
	result.DiscordantPairs = result.Wins + result.Losses
	result.OneSidedSignTestP = oneSidedSignTestP(result.Wins, result.Losses)
	if len(pairs) > 0 {
		result.SuccessRateDelta = float64(result.TreatmentPasses-result.BaselinePasses) / float64(len(pairs))
		result.MeanTokenDelta = float64(tokens) / float64(len(pairs))
	}
	return result
}

func classifyReport(report *Report) {
	report.Summary = summarize(report.Pairs)
	report.Authority = "exact_codex_cli_and_local_oracle_receipts"
	report.EfficacyClaim = "measurement_only"
	if report.Summary.VerifiedRetrievalPairs != len(report.Pairs) {
		report.Issues = append(report.Issues,
			"caller-provided memory context is diagnostic; observed benefit requires verified retrieval injections for every pair")
	}
	if report.Summary.ToolFreePairs != len(report.Pairs) {
		report.Issues = append(report.Issues,
			"observed benefit requires sealed tool_policy=forbid and zero tool calls for every pair")
	}
	if report.Summary.DistinctClusters < minimumDistinctTaskClusters {
		report.Issues = append(report.Issues, "fewer than 20 distinct task clusters; no observed-benefit claim")
	} else if len(report.Issues) == 0 && report.Summary.Wins > report.Summary.Losses &&
		report.Summary.TreatmentPasses > report.Summary.BaselinePasses && report.Summary.OneSidedSignTestP <= 0.05 {
		report.EfficacyClaim = "observed_benefit_on_sealed_suite_not_longitudinal_certification"
	}
	sort.Strings(report.Issues)
}

func oneSidedSignTestP(wins, losses int) float64 {
	n := wins + losses
	if n == 0 || wins <= losses {
		return 1
	}
	probability := 0.0
	for successes := wins; successes <= n; successes++ {
		coefficient := 1.0
		for factor := 1; factor <= successes; factor++ {
			coefficient *= float64(n-successes+factor) / float64(factor)
		}
		probability += coefficient * math.Pow(0.5, float64(n))
	}
	return probability
}

func createOutputRoot(root string) error {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(absolute, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("benchmark output root already exists")
		}
		return err
	}
	return nil
}

func writeExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func sealedPlanSHA256(plan SealedPlan) (string, error) {
	plan.PlanSHA256 = ""
	data, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

func reportSHA256(report Report) (string, error) {
	report.ReportSHA256 = ""
	data, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

func marshalIndented(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func decodeStrict(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("JSON has trailing data")
	}
	return nil
}

func commandOutput(ctx context.Context, timeout time.Duration, path string, args ...string) (string, error) {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := exec.CommandContext(commandContext, path, args...).CombinedOutput()
	return string(output), err
}

func commandExitCode(err error, contextErr error) int {
	if contextErr != nil {
		return 124
	}
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 126
}

func resolveUnder(root, relative string) (string, error) {
	target := filepath.Join(root, filepath.FromSlash(relative))
	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, absolute)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("benchmark artifact path escapes the plan directory")
	}
	return absolute, nil
}

func safeRelative(value string) bool {
	return value != "" && value != "." && !filepath.IsAbs(value) && !strings.Contains(value, "\\") &&
		!strings.Contains(value, ":") && pathpkg.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../")
}

func safeOracleArgument(value string) bool {
	return len(value) <= 4096 && !strings.ContainsAny(value, "/\\:\x00\r\n")
}

func environmentNames() []string {
	names := make([]string, 0, len(os.Environ()))
	for _, item := range os.Environ() {
		if index := strings.IndexByte(item, '='); index > 0 {
			names = append(names, strings.ToUpper(item[:index]))
		}
	}
	sort.Strings(names)
	return names
}

func environmentNamesSHA256() string {
	data, _ := json.Marshal(environmentNames())
	return sha256Hex(data)
}

func safeID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || strings.ContainsRune("._-", char) {
			continue
		}
		return false
	}
	return true
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
