package capturesupervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

var sourceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func ConfigureFile(store *ledger.Store, inputPath string, now time.Time) (status Status, returnErr error) {
	if store == nil {
		return status, errors.New("store is required")
	}
	if strings.TrimSpace(inputPath) == "" {
		return status, errors.New("capture supervisor config file is required")
	}
	absolute, err := filepath.Abs(inputPath)
	if err != nil {
		return status, fmt.Errorf("resolve capture supervisor config file: %w", err)
	}
	if err := rejectGitPath(absolute, "capture supervisor config input"); err != nil {
		return status, err
	}
	data, err := readLimited(absolute)
	if err != nil {
		return status, err
	}
	var config Config
	if err := decodeStrict(data, &config); err != nil {
		return status, errors.New("capture supervisor config is invalid JSON")
	}
	if err := normalizeAndValidateConfig(store, &config); err != nil {
		return status, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	lock, err := acquireOperationLock(store, now)
	if err != nil {
		return status, err
	}
	defer func() {
		if err := lock.Release(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("capture supervisor configured but lock cleanup failed: %w", err)
		}
	}()
	state, err := replayAudit(store)
	if err != nil {
		return status, err
	}
	if state.ActiveRunID != "" {
		return status, errors.New("an incomplete capture run must be recovered before configuration changes")
	}
	hash, encoded, err := encodeConfig(config)
	if err != nil {
		return status, err
	}
	if state.ConfigSHA256 == hash {
		return statusFromState(store, config, state, StatusOptions{RequireHealthy: true}), nil
	}
	path := filepath.Join(configRoot(store), hash+".json")
	if existing, err := readImmutable(store, path); err == nil {
		existingHash := sha256.Sum256(existing)
		if hex.EncodeToString(existingHash[:]) != hash || string(existing) != string(encoded) {
			return status, errors.New("capture supervisor config object does not match its content hash")
		}
	} else if errors.Is(err, os.ErrNotExist) || errors.Is(errors.Unwrap(err), os.ErrNotExist) {
		if err := writeImmutable(store, path, encoded); err != nil {
			return status, err
		}
	} else {
		return status, err
	}
	state, err = appendEvent(store, Event{
		ObservedAt: now, Action: "configure", Outcome: "configured", ConfigSHA256: hash,
	}, state)
	if err != nil {
		return status, err
	}
	return statusFromState(store, config, state, StatusOptions{RequireHealthy: true}), nil
}

func loadActiveConfig(store *ledger.Store, state replayState) (Config, error) {
	if state.ConfigSHA256 == "" {
		return Config{}, errors.New("capture supervisor is not configured")
	}
	path := filepath.Join(configRoot(store), state.ConfigSHA256+".json")
	data, err := readImmutable(store, path)
	if err != nil {
		return Config{}, err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != state.ConfigSHA256 {
		return Config{}, errors.New("capture supervisor config hash verification failed")
	}
	var config Config
	if err := decodeStrict(data, &config); err != nil {
		return Config{}, errors.New("capture supervisor config object is invalid")
	}
	if err := normalizeAndValidateConfig(store, &config); err != nil {
		return Config{}, err
	}
	normalizedHash, _, err := encodeConfig(config)
	if err != nil {
		return Config{}, err
	}
	if normalizedHash != state.ConfigSHA256 {
		return Config{}, errors.New("capture supervisor source paths no longer resolve to the configured locations")
	}
	return config, nil
}

func encodeConfig(config Config) (string, []byte, error) {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("encode capture supervisor config: %w", err)
	}
	data = append(data, '\n')
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), data, nil
}

func sourceSHA256(source Source) (string, error) {
	data, err := json.Marshal(source)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func normalizeAndValidateConfig(store *ledger.Store, config *Config) error {
	if config.SchemaVersion != ConfigSchemaVersion || config.Privacy != LocalPrivacy {
		return errors.New("capture supervisor config has an unsupported contract")
	}
	if config.IntervalSeconds < 5 || config.IntervalSeconds > 86400 {
		return errors.New("capture supervisor interval_seconds must be between 5 and 86400")
	}
	if config.FullReconcileEveryRuns < 1 || config.FullReconcileEveryRuns > 10000 {
		return errors.New("capture supervisor full_reconcile_every_runs must be between 1 and 10000")
	}
	if config.SourceTimeoutSeconds < 10 || config.SourceTimeoutSeconds > 86400 {
		return errors.New("capture supervisor source_timeout_seconds must be between 10 and 86400")
	}
	if len(config.Sources) == 0 || len(config.Sources) > 64 {
		return errors.New("capture supervisor config must contain between 1 and 64 sources")
	}
	ids := map[string]struct{}{}
	fingerprints := map[string]struct{}{}
	required := 0
	for index := range config.Sources {
		source := &config.Sources[index]
		if !sourceIDPattern.MatchString(source.ID) {
			return fmt.Errorf("capture supervisor source %d has an invalid id", index)
		}
		if _, duplicate := ids[source.ID]; duplicate {
			return fmt.Errorf("capture supervisor source id %q is duplicated", source.ID)
		}
		ids[source.ID] = struct{}{}
		if source.Required {
			required++
		}
		if err := normalizeAndValidateSource(store, source); err != nil {
			return fmt.Errorf("capture supervisor source %q: %w", source.ID, err)
		}
		identity := *source
		identity.ID = ""
		identity.Required = false
		fingerprint, err := sourceSHA256(identity)
		if err != nil {
			return err
		}
		if _, duplicate := fingerprints[fingerprint]; duplicate {
			return fmt.Errorf("capture supervisor source %q duplicates another source", source.ID)
		}
		fingerprints[fingerprint] = struct{}{}
	}
	if required == 0 {
		return errors.New("capture supervisor config must contain at least one required source")
	}
	sort.Slice(config.Sources, func(left, right int) bool {
		return config.Sources[left].ID < config.Sources[right].ID
	})
	return nil
}

func normalizeAndValidateSource(store *ledger.Store, source *Source) error {
	expectedAgent := ledger.AgentUnknown
	switch source.Kind {
	case SourceCodexRollouts:
		expectedAgent = ledger.AgentCodex
	case SourceClaudeHome:
		expectedAgent = ledger.AgentClaudeCode
	case SourceOpenCodeNative, SourceOpenCodeEvents:
		expectedAgent = ledger.AgentOpenCode
	default:
		return errors.New("kind is unsupported")
	}
	if source.Agent != expectedAgent {
		return errors.New("agent does not match source kind")
	}
	if source.Kind == SourceOpenCodeNative {
		if source.Path != "" || strings.TrimSpace(source.BinaryPath) == "" ||
			strings.TrimSpace(source.StagingRoot) == "" {
			return errors.New("opencode_native requires binary_path and staging_root only")
		}
		if !filepath.IsAbs(source.BinaryPath) || !filepath.IsAbs(source.StagingRoot) {
			return errors.New("binary_path and staging_root must be absolute")
		}
		binary, err := canonicalPath(source.BinaryPath)
		if err != nil {
			return fmt.Errorf("resolve binary_path: %w", err)
		}
		if err := rejectGitPath(binary, "OpenCode binary"); err != nil {
			return err
		}
		staging, err := canonicalPath(source.StagingRoot)
		if err != nil {
			return fmt.Errorf("resolve staging_root: %w", err)
		}
		if err := validateLocalSourcePath(store.Root(), staging, "OpenCode staging"); err != nil {
			return err
		}
		source.BinaryPath, source.StagingRoot = binary, staging
		return nil
	}
	if strings.TrimSpace(source.Path) == "" || source.BinaryPath != "" || source.StagingRoot != "" {
		return errors.New("source kind requires path only")
	}
	if !filepath.IsAbs(source.Path) {
		return errors.New("path must be absolute")
	}
	path, err := canonicalPath(source.Path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	if err := validateLocalSourcePath(store.Root(), path, "capture source"); err != nil {
		return err
	}
	source.Path = path
	return nil
}

func validateLocalSourcePath(evidenceRoot, sourcePath, label string) error {
	if err := rejectGitPath(sourcePath, label); err != nil {
		return err
	}
	evidence, err := canonicalPath(evidenceRoot)
	if err != nil {
		return err
	}
	if pathsOverlap(evidence, sourcePath) {
		return fmt.Errorf("%s and the evidence root must be separate", label)
	}
	return nil
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(absolute)
	missing := []string{}
	for {
		_, statErr := os.Lstat(current)
		if statErr == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(absolute), nil
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func rejectGitPath(path, label string) error {
	projected, err := canonicalPath(path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", label, err)
	}
	start := filepath.Clean(projected)
	if info, statErr := os.Lstat(start); statErr == nil {
		if !info.IsDir() {
			start = filepath.Dir(start)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect %s: %w", label, statErr)
	}
	for current := start; ; current = filepath.Dir(current) {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return fmt.Errorf("%s must be outside every Git worktree", label)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect %s Git boundary: %w", label, err)
		}
		if isBareGitDirectory(current) {
			return fmt.Errorf("%s must be outside every bare Git repository", label)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func isBareGitDirectory(path string) bool {
	head, headErr := os.Stat(filepath.Join(path, "HEAD"))
	objects, objectsErr := os.Stat(filepath.Join(path, "objects"))
	return headErr == nil && !head.IsDir() && objectsErr == nil && objects.IsDir()
}

func pathsOverlap(left, right string) bool {
	return pathWithin(left, right) || pathWithin(right, left)
}

func pathWithin(path, root string) bool {
	if runtime.GOOS == "windows" {
		path, root = strings.ToLower(path), strings.ToLower(root)
	}
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
