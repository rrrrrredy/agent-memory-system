package autosync

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	stateDirectoryName = "automatic-sync"
	configFileName     = "config.json"
	eventsFileName     = "events.jsonl"
	lockFileName       = "operation.lock"
	maximumEventBytes  = 1024 * 1024
)

type operationLock struct {
	path string
}

func stateDirectory(repositoryRoot string) string {
	return filepath.Join(repositoryRoot, ".agentmem", stateDirectoryName)
}

func configPath(repositoryRoot string) string {
	return filepath.Join(stateDirectory(repositoryRoot), configFileName)
}

func eventsPath(repositoryRoot string) string {
	return filepath.Join(stateDirectory(repositoryRoot), eventsFileName)
}

func acquireOperationLock(repositoryRoot string, now time.Time) (*operationLock, error) {
	directory := stateDirectory(repositoryRoot)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create automatic synchronization state directory: %w", err)
	}
	path := filepath.Join(directory, lockFileName)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, errors.New("automatic synchronization is locked; verify no run is active before explicit lock recovery")
	}
	if err != nil {
		return nil, fmt.Errorf("acquire automatic synchronization lock: %w", err)
	}
	record := struct {
		PID       int       `json:"pid"`
		CreatedAt time.Time `json:"created_at"`
	}{PID: os.Getpid(), CreatedAt: now.UTC()}
	encoder := json.NewEncoder(file)
	writeErr := encoder.Encode(record)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		if writeErr != nil {
			return nil, fmt.Errorf("write automatic synchronization lock: %w", writeErr)
		}
		return nil, fmt.Errorf("close automatic synchronization lock: %w", closeErr)
	}
	return &operationLock{path: path}, nil
}

func (lock *operationLock) Release() error {
	if lock == nil || lock.path == "" {
		return nil
	}
	if err := os.Remove(lock.path); err != nil {
		return err
	}
	lock.path = ""
	return nil
}

func clearOperationLock(repositoryRoot string) (bool, error) {
	path := filepath.Join(stateDirectory(repositoryRoot), lockFileName)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("remove automatic synchronization lock: %w", err)
	}
	return true, nil
}

func loadConfig(repositoryRoot string) (Config, bool, error) {
	path := configPath(repositoryRoot)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("read automatic synchronization config: %w", err)
	}
	var config Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, true, errors.New("automatic synchronization config is invalid")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Config{}, true, errors.New("automatic synchronization config contains trailing data")
	}
	return config, true, nil
}

func writeConfig(repositoryRoot string, config Config) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode automatic synchronization config: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(stateDirectory(repositoryRoot), 0o700); err != nil {
		return fmt.Errorf("create automatic synchronization state directory: %w", err)
	}
	return atomicWrite(configPath(repositoryRoot), data, 0o600)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".automatic-sync-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("set state file permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write temporary state file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary state file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary state file: %w", err)
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	keep = true
	return nil
}

func configSHA256(config Config) (string, error) {
	data, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func replayEvents(repositoryRoot string) (State, error) {
	state := State{State: StateDisabled}
	file, err := os.Open(eventsPath(repositoryRoot))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("open automatic synchronization audit: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maximumEventBytes)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(bytes.TrimSpace(line)) == 0 {
			return state, errors.New("automatic synchronization audit contains an empty record")
		}
		var event Event
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil {
			return state, errors.New("automatic synchronization audit contains an invalid record")
		}
		if err := requireJSONEOF(decoder); err != nil {
			return state, errors.New("automatic synchronization audit record contains trailing data")
		}
		if err := validateEvent(event, state); err != nil {
			return state, err
		}
		state = stateFromEvent(event)
	}
	if err := scanner.Err(); err != nil {
		return state, fmt.Errorf("read automatic synchronization audit: %w", err)
	}
	return state, nil
}

func appendEvent(repositoryRoot string, event Event, prior State) (State, error) {
	event.SchemaVersion = EventSchemaVersion
	event.Sequence = prior.LastSequence + 1
	event.ObservedAt = event.ObservedAt.UTC()
	event.PreviousEventSHA256 = prior.LastEventSHA256
	event.Privacy = LocalPrivacy
	event.EventSHA256 = ""
	hash, err := eventSHA256(event)
	if err != nil {
		return prior, err
	}
	event.EventSHA256 = hash
	if err := validateEvent(event, prior); err != nil {
		return prior, err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return prior, fmt.Errorf("encode automatic synchronization event: %w", err)
	}
	if err := os.MkdirAll(stateDirectory(repositoryRoot), 0o700); err != nil {
		return prior, fmt.Errorf("create automatic synchronization state directory: %w", err)
	}
	file, err := os.OpenFile(eventsPath(repositoryRoot), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return prior, fmt.Errorf("open automatic synchronization audit: %w", err)
	}
	data = append(data, '\n')
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return prior, fmt.Errorf("append automatic synchronization audit: %w", writeErr)
	}
	if closeErr != nil {
		return prior, fmt.Errorf("close automatic synchronization audit: %w", closeErr)
	}
	return stateFromEvent(event), nil
}

func eventSHA256(event Event) (string, error) {
	event.EventSHA256 = ""
	data, err := json.Marshal(event)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func validateEvent(event Event, prior State) error {
	if event.SchemaVersion != EventSchemaVersion || event.Privacy != LocalPrivacy {
		return errors.New("automatic synchronization audit record has an unsupported contract")
	}
	if event.Sequence != prior.LastSequence+1 || event.PreviousEventSHA256 != prior.LastEventSHA256 {
		return errors.New("automatic synchronization audit sequence is broken")
	}
	if event.ObservedAt.IsZero() || !validState(event.ResultingState) || event.ConsecutiveErrors < 0 {
		return errors.New("automatic synchronization audit record has invalid state")
	}
	if event.ConfigSHA256 != "" && !validSHA256(event.ConfigSHA256) {
		return errors.New("automatic synchronization audit record has an invalid config hash")
	}
	expected, err := eventSHA256(event)
	if err != nil || expected != event.EventSHA256 {
		return errors.New("automatic synchronization audit hash verification failed")
	}
	switch event.Action {
	case "enable":
		if event.ResultingState != StateReady || event.ConsecutiveErrors != 0 || event.ConfigSHA256 == "" {
			return errors.New("automatic synchronization enable event is invalid")
		}
	case "disable":
		if event.ResultingState != StateDisabled || event.ConsecutiveErrors != 0 || event.ConfigSHA256 == "" {
			return errors.New("automatic synchronization disable event is invalid")
		}
	case "recover":
		if event.ResultingState != StateReady || event.ConsecutiveErrors != 0 || event.ConfigSHA256 == "" {
			return errors.New("automatic synchronization recovery event is invalid")
		}
	case "attempt":
		if event.LastAttemptAt == nil || event.LastSyncOutcome == "" || event.ConfigSHA256 == "" || event.Export == nil {
			return errors.New("automatic synchronization attempt event is incomplete")
		}
		if event.LastSyncOutcome != "export_failed" && event.Sync == nil {
			return errors.New("automatic synchronization attempt has no Git result")
		}
		if event.ResultingState == StateReady && event.ConsecutiveErrors != 0 {
			return errors.New("successful automatic synchronization retained an error count")
		}
		if event.ResultingState == StateRetrying && (event.ConsecutiveErrors == 0 || event.NextAttemptAt == nil) {
			return errors.New("automatic synchronization retry event is incomplete")
		}
		if event.ResultingState == StateSuspended && event.ErrorCode == "" {
			return errors.New("automatic synchronization suspension has no reason")
		}
		if event.ResultingState != StateReady && event.ErrorDetail == "" {
			return errors.New("failed automatic synchronization attempt has no local error detail")
		}
	default:
		return errors.New("automatic synchronization audit action is invalid")
	}
	return nil
}

func stateFromEvent(event Event) State {
	return State{
		State: event.ResultingState, ConsecutiveErrors: event.ConsecutiveErrors,
		NextAttemptAt: event.NextAttemptAt, LastAttemptAt: event.LastAttemptAt,
		LastSuccessAt: event.LastSuccessAt, LastSyncOutcome: event.LastSyncOutcome,
		ErrorCode: event.ErrorCode, LastSequence: event.Sequence,
		LastEventSHA256: event.EventSHA256, ConfigSHA256: event.ConfigSHA256,
	}
}

func validState(value string) bool {
	return value == StateDisabled || value == StateReady || value == StateRetrying || value == StateSuspended
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("extra JSON value")
	}
	return err
}
