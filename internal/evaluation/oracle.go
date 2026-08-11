package evaluation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	maximumOracleOutputBytes   = 1024 * 1024
	maximumOracleRegistryBytes = 4 * 1024 * 1024
)

type loadedOracleRegistry struct {
	Registry OracleRegistry
	SHA256   string
	Entries  map[string]OracleRegistryEntry
	Data     []byte
}

func loadOracleRegistry(path string) (loadedOracleRegistry, error) {
	if strings.TrimSpace(path) == "" {
		return loadedOracleRegistry{}, errors.New("oracle registry path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return loadedOracleRegistry{}, fmt.Errorf("read oracle registry: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximumOracleRegistryBytes+1))
	if err != nil {
		return loadedOracleRegistry{}, fmt.Errorf("read oracle registry: %w", err)
	}
	if len(data) == 0 || len(data) > maximumOracleRegistryBytes {
		return loadedOracleRegistry{}, errors.New("oracle registry is empty or exceeds the local safety limit")
	}
	return decodeOracleRegistry(data)
}

func decodeOracleRegistry(data []byte) (loadedOracleRegistry, error) {
	if len(data) == 0 || len(data) > maximumOracleRegistryBytes {
		return loadedOracleRegistry{}, errors.New("oracle registry is empty or exceeds the local safety limit")
	}
	var registry OracleRegistry
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		return loadedOracleRegistry{}, fmt.Errorf("decode oracle registry: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return loadedOracleRegistry{}, err
	}
	if registry.SchemaVersion != OracleRegistrySchema || registry.Privacy != "local_only" || len(registry.Entries) == 0 {
		return loadedOracleRegistry{}, errors.New("oracle registry envelope is invalid")
	}
	entries := make(map[string]OracleRegistryEntry, len(registry.Entries))
	previous := ""
	for _, entry := range registry.Entries {
		key := oracleRegistryKey(entry.Kind, entry.ID, entry.Version)
		if key <= previous {
			return loadedOracleRegistry{}, errors.New("oracle registry entries must be sorted and unique")
		}
		switch entry.Kind {
		case "harness":
			if !safeIdentifier(entry.ID) || strings.TrimSpace(entry.Version) == "" ||
				!filepath.IsAbs(entry.Executable) || !validSHA256(entry.ExecutableSHA256) ||
				entry.TimeoutSeconds < 1 || entry.TimeoutSeconds > 600 || entry.Arguments == nil {
				return loadedOracleRegistry{}, errors.New("native harness oracle registry entry is invalid")
			}
			for _, argument := range entry.Arguments {
				if strings.IndexByte(argument, 0) >= 0 || len(argument) > 16*1024 {
					return loadedOracleRegistry{}, errors.New("oracle registry contains an invalid argument")
				}
			}
			if err := verifyExecutable(entry); err != nil {
				return loadedOracleRegistry{}, err
			}
		case "builtin":
			if entry.ID != "evidence-score" || entry.Version != "v1" || entry.Executable != "" ||
				entry.ExecutableSHA256 != "" || len(entry.Arguments) != 0 || entry.TimeoutSeconds != 0 {
				return loadedOracleRegistry{}, errors.New("builtin evidence-score oracle registry entry is invalid")
			}
		default:
			return loadedOracleRegistry{}, errors.New("oracle registry entry kind is unsupported")
		}
		entries[key] = entry
		previous = key
	}
	digest := sha256.Sum256(data)
	return loadedOracleRegistry{Registry: registry, SHA256: hex.EncodeToString(digest[:]),
		Entries: entries, Data: append([]byte(nil), data...)}, nil
}

func loadOracleRegistryBlob(store *ledger.Store, reference ledger.BlobRef) (loadedOracleRegistry, error) {
	data, err := readAttemptBlob(store, reference)
	if err != nil {
		return loadedOracleRegistry{}, fmt.Errorf("read oracle registry snapshot: %w", err)
	}
	registry, err := decodeOracleRegistry(data)
	if err != nil {
		return loadedOracleRegistry{}, err
	}
	if registry.SHA256 != reference.SHA256 {
		return loadedOracleRegistry{}, errors.New("oracle registry snapshot hash mismatch")
	}
	return registry, nil
}

func verifyExecutable(entry OracleRegistryEntry) error {
	info, err := os.Lstat(entry.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("oracle executable %q is unavailable or not a regular file", entry.ID)
	}
	file, err := os.Open(entry.Executable)
	if err != nil {
		return fmt.Errorf("open oracle executable %q: %w", entry.ID, err)
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return fmt.Errorf("hash oracle executable %q: %w", entry.ID, err)
	}
	if hex.EncodeToString(hasher.Sum(nil)) != entry.ExecutableSHA256 {
		return fmt.Errorf("oracle executable %q hash mismatch", entry.ID)
	}
	return nil
}

func oracleRegistryKey(kind, id, version string) string {
	return kind + "\x00" + id + "\x00" + version
}

func oracleEntrySHA256(entry OracleRegistryEntry) string {
	data, _ := json.Marshal(entry)
	return sha256Hex(data)
}

func BuiltinEvidenceScoreRegistry() (OracleRegistry, string) {
	entry := OracleRegistryEntry{Kind: "builtin", ID: "evidence-score", Version: "v1",
		Executable: "", ExecutableSHA256: "", Arguments: []string{}, TimeoutSeconds: 0}
	return OracleRegistry{SchemaVersion: OracleRegistrySchema,
		Entries: []OracleRegistryEntry{entry}, Privacy: "local_only"}, oracleEntrySHA256(entry)
}

func rerunTaskAttempt(store *ledger.Store, receipt TaskAttemptReceipt,
	records map[string]indexedRecord, ordered []ledger.Record, registry loadedOracleRegistry) error {
	record, exists := records[receipt.ReceiptID]
	if !exists {
		return errors.New("task attempt receipt is unavailable")
	}
	if verification := verifyTaskAttemptRecord(store, record.Record, records, ordered); len(verification.Issues) != 0 {
		return errors.New("task attempt evidence replay failed: " + strings.Join(verification.Issues, "; "))
	}
	if (receipt.Oracle.Kind != "harness" && receipt.Oracle.Kind != "builtin") ||
		!validSHA256(receipt.Oracle.RegistryEntrySHA256) {
		return errors.New("task attempt is not bound to a runnable oracle registry entry")
	}
	entry, exists := registry.Entries[oracleRegistryKey(receipt.Oracle.Kind, receipt.Oracle.ID, receipt.Oracle.Version)]
	if !exists || oracleEntrySHA256(entry) != receipt.Oracle.RegistryEntrySHA256 {
		return errors.New("task attempt oracle registry binding is unavailable or changed")
	}
	if entry.Kind == "harness" {
		if err := verifyExecutable(entry); err != nil {
			return err
		}
	}
	if receipt.TaskSpecBlob == nil || receipt.AcceptanceCriteriaBlob == nil || receipt.ExecutionConfigBlob == nil {
		return errors.New("task attempt exact task artifacts are unavailable")
	}
	request := taskAttemptRequestFromReceipt(receipt)
	if err := validateAttemptBlobs(store, request); err != nil {
		return err
	}
	input := OracleReplayInput{
		SchemaVersion: OracleReplayInputSchema, TaskID: receipt.TaskID, AttemptID: receipt.AttemptID,
		Condition: receipt.Condition, Privacy: "local_only",
	}
	var err error
	if input.TaskSpec, err = readAttemptBlob(store, *receipt.TaskSpecBlob); err != nil {
		return err
	}
	if input.AcceptanceCriteria, err = readAttemptBlob(store, *receipt.AcceptanceCriteriaBlob); err != nil {
		return err
	}
	if input.ExecutionConfig, err = readAttemptBlob(store, *receipt.ExecutionConfigBlob); err != nil {
		return err
	}
	orderedReferences := make([]BoundEventReference, 0, len(receipt.ResultEvents)+len(receipt.UserMessages))
	orderedReferences = append(orderedReferences, receipt.ResultEvents...)
	orderedReferences = append(orderedReferences, receipt.UserMessages...)
	if input.OrderedEvents, err = replayEvents(store, orderedReferences, records); err != nil {
		return err
	}
	if input.ResultEvents, err = replayEvents(store, receipt.ResultEvents, records); err != nil {
		return err
	}
	if input.UserMessages, err = replayEvents(store, receipt.UserMessages, records); err != nil {
		return err
	}
	if entry.Kind == "builtin" {
		judgment, err := runBuiltinEvidenceScoreOracle(input.AcceptanceCriteria, input.OrderedEvents,
			input.ResultEvents, input.UserMessages)
		if err != nil {
			return err
		}
		replayed := materializeBuiltinVerdict(request, judgment, input.ResultEvents, input.UserMessages)
		return verifyReplayedVerdict(store, replayed, receipt, records)
	}
	inputData, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode oracle replay input: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(entry.TimeoutSeconds)*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, entry.Executable, entry.Arguments...)
	command.Env = []string{}
	command.Stdin = bytes.NewReader(inputData)
	stdout, stderr := &boundedBuffer{maximum: maximumOracleOutputBytes}, &boundedBuffer{maximum: maximumOracleOutputBytes}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return errors.New("oracle replay timed out")
		}
		return fmt.Errorf("oracle replay failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.overflow || stderr.overflow {
		return errors.New("oracle replay output exceeded the local safety limit")
	}
	var replayed TaskAttemptVerdict
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&replayed); err != nil {
		return fmt.Errorf("decode oracle replay verdict: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	return verifyReplayedVerdict(store, replayed, receipt, records)
}

func readAttemptBlob(store *ledger.Store, reference ledger.BlobRef) ([]byte, error) {
	file, err := store.OpenBlob(reference)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != reference.Bytes || sha256Hex(data) != reference.SHA256 {
		return nil, errors.New("task artifact blob changed during oracle replay")
	}
	return data, nil
}

func replayEvents(store *ledger.Store, references []BoundEventReference,
	records map[string]indexedRecord) ([]OracleReplayEvent, error) {
	result := make([]OracleReplayEvent, 0, len(references))
	for _, reference := range references {
		record, exists := records[reference.EventID]
		if !exists || boundReference(record.Record) != reference {
			return nil, fmt.Errorf("oracle replay event %q is unavailable or changed", reference.EventID)
		}
		payload, err := eventPayload(store, record.Record.Event)
		if err != nil {
			return nil, err
		}
		result = append(result, OracleReplayEvent{EventID: reference.EventID,
			Kind: record.Record.Event.Kind, Payload: payload, RecordHash: reference.RecordHash})
	}
	sort.Slice(result, func(i, j int) bool {
		return records[result[i].EventID].Index < records[result[j].EventID].Index
	})
	return result, nil
}

type boundedBuffer struct {
	data     bytes.Buffer
	maximum  int
	overflow bool
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := buffer.maximum - buffer.data.Len()
	if remaining <= 0 {
		buffer.overflow = true
		return written, nil
	}
	if len(data) > remaining {
		buffer.overflow = true
		data = data[:remaining]
	}
	_, _ = buffer.data.Write(data)
	return written, nil
}

func (buffer *boundedBuffer) Bytes() []byte  { return buffer.data.Bytes() }
func (buffer *boundedBuffer) String() string { return buffer.data.String() }
