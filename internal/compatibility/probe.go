package compatibility

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/agentbridge"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const SchemaVersion = "agent-compatibility-report/v1alpha2"

type RuntimeStatus string

const (
	RuntimeAvailable RuntimeStatus = "available"
	RuntimeBlocked   RuntimeStatus = "blocked"
	RuntimeNotFound  RuntimeStatus = "not_found"
)

type AgentReport struct {
	Agent                         ledger.Agent  `json:"agent"`
	Command                       string        `json:"command"`
	RuntimeStatus                 RuntimeStatus `json:"runtime_status"`
	RuntimeIssue                  string        `json:"runtime_issue,omitempty"`
	Version                       string        `json:"version,omitempty"`
	ExecutableSHA256              string        `json:"executable_sha256,omitempty"`
	HistoryImportAvailable        bool          `json:"history_import_available"`
	CaptureModes                  []string      `json:"capture_modes"`
	RetrievalModes                []string      `json:"retrieval_modes"`
	NativeExecutionVerified       bool          `json:"native_execution_verified"`
	ExecutionEvidence             string        `json:"execution_evidence"`
	ProviderIndependentlyAttested bool          `json:"provider_independently_attested"`
	Limitations                   []string      `json:"limitations"`
}

type Report struct {
	SchemaVersion string        `json:"schema_version"`
	GeneratedAt   time.Time     `json:"generated_at"`
	Ready         bool          `json:"ready"`
	Agents        []AgentReport `json:"agents"`
	Privacy       string        `json:"privacy"`
}

type Options struct {
	Agents        []ledger.Agent
	Timeout       time.Duration
	Now           func() time.Time
	LookPath      func(string) (string, error)
	RunVersion    func(context.Context, string) ([]byte, error)
	ReadFile      func(string) ([]byte, error)
	EvidenceStore *ledger.Store
}

func Probe(ctx context.Context, options Options) Report {
	options = defaults(options)
	agents := normalizedAgents(options.Agents)
	report := Report{SchemaVersion: SchemaVersion, GeneratedAt: options.Now().UTC(),
		Ready: true, Agents: make([]AgentReport, 0, len(agents)), Privacy: "local_only"}
	for _, agent := range agents {
		item := integrationContract(agent)
		path, err := options.LookPath(item.Command)
		if err != nil {
			item.RuntimeStatus = RuntimeNotFound
			item.RuntimeIssue = "command_not_found"
			report.Ready = false
			report.Agents = append(report.Agents, item)
			continue
		}
		data, readErr := options.ReadFile(path)
		if readErr == nil {
			digest := sha256.Sum256(data)
			item.ExecutableSHA256 = hex.EncodeToString(digest[:])
		}
		runVersion := options.RunVersion
		runPath := path
		cleanup := func() {}
		if runVersion == nil {
			if readErr != nil {
				item.RuntimeStatus = RuntimeBlocked
				item.RuntimeIssue = "executable_read_failed"
				report.Ready = false
				report.Agents = append(report.Agents, item)
				continue
			}
			staged, remove, stageErr := stageVersionProbe(data, filepath.Ext(path))
			if stageErr != nil {
				item.RuntimeStatus = RuntimeBlocked
				item.RuntimeIssue = "version_probe_staging_failed"
				report.Ready = false
				report.Agents = append(report.Agents, item)
				continue
			}
			runPath, cleanup = staged, remove
			runVersion = func(ctx context.Context, executable string) ([]byte, error) {
				return exec.CommandContext(ctx, executable, "--version").CombinedOutput()
			}
		}
		versionContext, cancel := context.WithTimeout(ctx, options.Timeout)
		output, runErr := runVersion(versionContext, runPath)
		deadline := errors.Is(versionContext.Err(), context.DeadlineExceeded)
		cancel()
		cleanup()
		if deadline {
			item.RuntimeStatus = RuntimeBlocked
			item.RuntimeIssue = "version_probe_timeout"
			report.Ready = false
		} else if runErr != nil {
			item.RuntimeStatus = RuntimeBlocked
			item.RuntimeIssue = "version_probe_failed"
			report.Ready = false
		} else if item.Version = normalizedVersion(output); item.Version == "" {
			item.RuntimeStatus = RuntimeBlocked
			item.RuntimeIssue = "empty_version_output"
			report.Ready = false
		} else {
			item.RuntimeStatus = RuntimeAvailable
		}
		if agent == ledger.AgentCodex && options.EvidenceStore != nil {
			executions, verifyErr := agentbridge.ListVerifiedExecutions(options.EvidenceStore)
			if verifyErr == nil && item.ExecutableSHA256 != "" {
				for _, execution := range executions {
					if execution.Started.CodexExecutable.SHA256 == item.ExecutableSHA256 {
						item.NativeExecutionVerified = true
						break
					}
				}
			}
			if item.NativeExecutionVerified {
				item.ExecutionEvidence = "local_replayable_receipt"
				item.Limitations = codexVerifiedLimitations()
			} else if verifyErr == nil && len(executions) > 0 {
				item.Limitations = append(item.Limitations, "verified native receipts do not bind the currently probed Codex executable bytes")
			}
		}
		report.Agents = append(report.Agents, item)
	}
	return report
}

func defaults(options Options) Options {
	if len(options.Agents) == 0 {
		options.Agents = []ledger.Agent{ledger.AgentCodex, ledger.AgentClaudeCode, ledger.AgentOpenCode}
	}
	if options.Timeout <= 0 {
		options.Timeout = 5 * time.Second
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.LookPath == nil {
		options.LookPath = exec.LookPath
	}
	if options.ReadFile == nil {
		options.ReadFile = os.ReadFile
	}
	return options
}

func stageVersionProbe(data []byte, suffix string) (string, func(), error) {
	directory, err := os.MkdirTemp("", "agentmem-compatibility-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	if suffix != "" && strings.ContainsAny(suffix, `/\\`) {
		cleanup()
		return "", func() {}, errors.New("runtime executable suffix is invalid")
	}
	path := filepath.Join(directory, "runtime"+suffix)
	if err := os.WriteFile(path, data, 0o700); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

func normalizedAgents(values []ledger.Agent) []ledger.Agent {
	seen := map[ledger.Agent]struct{}{}

	result := make([]ledger.Agent, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func normalizedVersion(output []byte) string {
	if len(output) > 4096 {
		output = output[:4096]
	}
	return strings.Join(strings.Fields(string(output)), " ")
}

func integrationContract(agent ledger.Agent) AgentReport {
	item := AgentReport{Agent: agent, HistoryImportAvailable: true,
		CaptureModes: []string{}, RetrievalModes: []string{},
		NativeExecutionVerified: false, ProviderIndependentlyAttested: false,
		Limitations: []string{"provider-hidden reasoning cannot be recovered"}}
	switch agent {
	case ledger.AgentCodex:
		item.Command = "codex"
		item.CaptureModes = []string{"history_reconciliation", "lifecycle_hook_spool"}
		item.RetrievalModes = []string{"cli_injection", "mcp"}
		item.ExecutionEvidence = "not_verified"
		item.Limitations = append(item.Limitations,
			"native execution is unverified until an evidence root contains a replayable receipt",
			"a local process receipt does not independently attest the remote provider or model")
	case ledger.AgentClaudeCode:
		item.Command = "claude"
		item.CaptureModes = []string{"history_reconciliation", "lifecycle_hook_spool"}
		item.RetrievalModes = []string{"cli_injection", "mcp"}
		item.ExecutionEvidence = "adapter_protocol_only"
		item.Limitations = append(item.Limitations,
			"Claude Code coverage is adapter and hook protocol conformance only",
			"no live Claude provider execution is attested by this report")
	case ledger.AgentOpenCode:
		item.Command = "opencode"
		item.CaptureModes = []string{"native_export_reconciliation", "plugin_event_spool"}
		item.RetrievalModes = []string{"plugin_injection", "mcp"}
		item.ExecutionEvidence = "hosted_runtime_smoke_only"
		item.Limitations = append(item.Limitations,
			"OpenCode runtime verification is hosted-only and requires no local installation",
			"the hosted smoke does not invoke a provider model")
	default:
		item.Command = string(agent)
		item.HistoryImportAvailable = false
		item.ExecutionEvidence = "unsupported"
		item.Limitations = append(item.Limitations, "unsupported Agent integration")
	}
	return item
}

func codexVerifiedLimitations() []string {
	return []string{
		"provider-hidden reasoning cannot be recovered",
		"the receipt attests exact local Codex CLI bytes, arguments, exposed JSONL, and output only",
		"the remote provider and selected model are not independently attested",
	}
}
