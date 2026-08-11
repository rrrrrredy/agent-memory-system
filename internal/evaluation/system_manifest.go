package evaluation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

// CurrentSystemArtifactSHA256 binds evaluation trials to the exact executable
// and embedded Go build information that recorded or evaluates them.
func CurrentSystemArtifactSHA256() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve system executable: %w", err)
	}
	file, err := os.Open(executable)
	if err != nil {
		return "", fmt.Errorf("open system executable: %w", err)
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("hash system executable: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close system executable: %w", err)
	}
	executableSHA256 := hex.EncodeToString(hasher.Sum(nil))
	buildText := "unavailable"
	if build, ok := debug.ReadBuildInfo(); ok {
		buildText = build.String()
	}
	buildDigest := sha256.Sum256([]byte(buildText))
	manifest := struct {
		SchemaVersion    string `json:"schema_version"`
		ExecutableSHA256 string `json:"executable_sha256"`
		BuildInfoSHA256  string `json:"build_info_sha256"`
		GOOS             string `json:"goos"`
		GOARCH           string `json:"goarch"`
	}{
		SchemaVersion: "system-artifact-manifest/v1alpha1", ExecutableSHA256: executableSHA256,
		BuildInfoSHA256: hex.EncodeToString(buildDigest[:]), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("encode system artifact manifest: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func bindCurrentSystemArtifact(request *TaskAttemptRequest) error {
	current, err := CurrentSystemArtifactSHA256()
	if err != nil {
		return err
	}
	if request.SystemArtifactSHA256 != "" && request.SystemArtifactSHA256 != current {
		return fmt.Errorf("task attempt system artifact does not match the current executable")
	}
	request.SystemArtifactSHA256 = current
	return nil
}

// BindSystemUnderTest stores the exact prompt, tool registry, harness, and
// adapter artifacts in the local evidence store and returns their manifest.
func BindSystemUnderTest(store *ledger.Store, agent ledger.Agent, provider, model string,
	artifacts SystemUnderTestArtifacts) (SystemUnderTestManifest, error) {
	if store == nil {
		return SystemUnderTestManifest{}, fmt.Errorf("store is required")
	}
	if len(artifacts.SystemPrompt) == 0 || len(artifacts.ToolRegistry) == 0 ||
		len(artifacts.Harness) == 0 || len(artifacts.Adapter) == 0 {
		return SystemUnderTestManifest{}, fmt.Errorf(
			"system prompt, tool registry, harness, and adapter artifacts must be non-empty")
	}
	if !validAgent(agent) || agent == ledger.AgentUnknown || strings.TrimSpace(provider) == "" ||
		strings.TrimSpace(model) == "" || len(provider) > 160 || len(model) > 160 {
		return SystemUnderTestManifest{}, fmt.Errorf("agent, provider, and model are required")
	}
	put := func(data []byte) (ledger.BlobRef, error) {
		return store.PutBlob(bytes.NewReader(data))
	}
	systemPrompt, err := put(artifacts.SystemPrompt)
	if err != nil {
		return SystemUnderTestManifest{}, fmt.Errorf("store system prompt artifact: %w", err)
	}
	toolRegistry, err := put(artifacts.ToolRegistry)
	if err != nil {
		return SystemUnderTestManifest{}, fmt.Errorf("store tool registry artifact: %w", err)
	}
	harness, err := put(artifacts.Harness)
	if err != nil {
		return SystemUnderTestManifest{}, fmt.Errorf("store harness artifact: %w", err)
	}
	adapter, err := put(artifacts.Adapter)
	if err != nil {
		return SystemUnderTestManifest{}, fmt.Errorf("store adapter artifact: %w", err)
	}
	manifest := SystemUnderTestManifest{
		SchemaVersion: SystemUnderTestManifestSchema, Agent: agent,
		Provider: provider, Model: model,
		SystemPromptSHA256: systemPrompt.SHA256, SystemPromptBlob: systemPrompt,
		ToolRegistrySHA256: toolRegistry.SHA256, ToolRegistryBlob: toolRegistry,
		HarnessSHA256: harness.SHA256, HarnessBlob: harness,
		AdapterSHA256: adapter.SHA256, AdapterBlob: adapter,
		Privacy: "local_only",
	}
	if err := validateSystemUnderTestManifest(store, manifest, agent); err != nil {
		return SystemUnderTestManifest{}, err
	}
	return manifest, nil
}

func validateSystemUnderTestManifest(store *ledger.Store, manifest SystemUnderTestManifest,
	expectedAgent ledger.Agent) error {
	if manifest.SchemaVersion != SystemUnderTestManifestSchema || manifest.Agent != expectedAgent ||
		strings.TrimSpace(manifest.Provider) == "" || strings.TrimSpace(manifest.Model) == "" ||
		len(manifest.Provider) > 160 || len(manifest.Model) > 160 || manifest.Privacy != "local_only" {
		return fmt.Errorf("task attempt system-under-test manifest is invalid")
	}
	items := []struct {
		name string
		hash string
		blob ledger.BlobRef
	}{
		{"system prompt", manifest.SystemPromptSHA256, manifest.SystemPromptBlob},
		{"tool registry", manifest.ToolRegistrySHA256, manifest.ToolRegistryBlob},
		{"harness", manifest.HarnessSHA256, manifest.HarnessBlob},
		{"adapter", manifest.AdapterSHA256, manifest.AdapterBlob},
	}
	for _, item := range items {
		if !validSHA256(item.hash) || !validBlobRef(item.blob) || item.blob.SHA256 != item.hash ||
			item.blob.Bytes == 0 || verifyBlob(store, item.blob) != nil {
			return fmt.Errorf("task attempt system-under-test %s artifact is invalid", item.name)
		}
	}
	return nil
}
