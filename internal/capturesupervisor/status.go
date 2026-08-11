package capturesupervisor

import (
	"sort"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func GetStatus(store *ledger.Store, options StatusOptions) (Status, error) {
	status := Status{
		SchemaVersion: StatusSchemaVersion, Sources: []SourceStatus{}, Issues: []Issue{},
		Warnings: []Issue{}, IntegrityReady: true, Privacy: LocalPrivacy,
	}
	strictCapture := captureHealthRequired(options)
	if store == nil {
		status.IntegrityReady = false
		status.Issues = append(status.Issues, Issue{
			Code: "store_required", Message: "local evidence store is required",
		})
		return finalizeStatus(status), nil
	}
	state, err := replayAudit(store)
	if err != nil {
		status.IntegrityReady = false
		status.Issues = append(status.Issues, Issue{
			Code:      "capture_supervisor_audit_invalid",
			Message:   "capture supervisor audit verification failed",
			RecoverBy: "restore the local evidence backup or inspect the append-only audit",
		})
		return finalizeStatus(status), nil
	}
	status.AuditEventsChecked = state.LastSequence
	status.Warnings = append(status.Warnings, state.Warnings...)
	if locked, err := operationLockHeld(store); err == nil && locked {
		status.OperationLocked = true
		status.IntegrityReady = false
		status.Issues = append(status.Issues, Issue{
			Code:      "capture_operation_locked",
			Message:   "a capture operation is active or left a stale lock",
			RecoverBy: "wait for the active operation or explicitly recover after verifying it stopped",
		})
	} else if err != nil {
		status.IntegrityReady = false
		status.Issues = append(status.Issues, Issue{
			Code: "capture_lock_unavailable", Message: "capture operation lock state could not be inspected",
		})
	}
	if state.ConfigSHA256 == "" {
		if options.RequireConfigured || strictCapture {
			status.Issues = append(status.Issues, Issue{
				Code:    "capture_supervisor_not_configured",
				Message: "capture supervision is required but has no local configuration",
			})
		}
		status.Ready = status.IntegrityReady && !options.RequireConfigured && !strictCapture
		return finalizeStatus(status), nil
	}
	config, err := loadActiveConfig(store, state)
	if err != nil {
		status.IntegrityReady = false
		status.Issues = append(status.Issues, Issue{
			Code:      "capture_supervisor_config_invalid",
			Message:   "capture supervisor configuration verification failed",
			RecoverBy: "restore or replace the local configuration with an explicit configure operation",
		})
		return finalizeStatus(status), nil
	}
	status = statusFromState(store, config, state, options)
	if locked, err := operationLockHeld(store); err == nil && locked {
		status.OperationLocked = true
		status.IntegrityReady = false
		status.CaptureReady = false
		status.Issues = appendIssueOnce(status.Issues, Issue{
			Code:      "capture_operation_locked",
			Message:   "a capture operation is active or left a stale lock",
			RecoverBy: "wait for the active operation or explicitly clear the lock after verifying no process is active",
		})
	} else if err != nil {
		status.IntegrityReady = false
		status.CaptureReady = false
		status.Issues = appendIssueOnce(status.Issues, Issue{
			Code:    "capture_lock_unavailable",
			Message: "capture operation lock state could not be inspected",
		})
	}
	status.Ready = status.IntegrityReady && (!strictCapture || status.CaptureReady)
	return finalizeStatus(status), nil
}

func statusFromState(
	_ *ledger.Store, config Config, state replayState, options StatusOptions,
) Status {
	if options.Now == nil {
		options.Now = time.Now
	}
	status := Status{
		SchemaVersion: StatusSchemaVersion, Configured: true, ConfigSHA256: state.ConfigSHA256,
		AuditEventsChecked: state.LastSequence, RunsCompleted: state.RunsCompleted,
		LastRunID: state.LastRunID, LastRunStartedAt: state.LastRunStartedAt,
		LastRunFinishedAt: state.LastRunFinishedAt, LastRunOutcome: state.LastRunOutcome,
		RunIncomplete: state.ActiveRunID != "", Sources: []SourceStatus{},
		IntegrityReady: true, Issues: []Issue{}, Warnings: append([]Issue{}, state.Warnings...),
		Privacy: LocalPrivacy,
	}
	status.Warnings = appendIssueOnce(status.Warnings, Issue{
		Code:    "coverage_unproven",
		Message: "capture covers locally observable sources but cannot prove that an unexposed source never existed",
	})
	requiredAgents := uniqueAgents(options.RequiredAgents)
	maximumAge := options.MaximumAge
	if maximumAge < 0 {
		status.Issues = append(status.Issues, Issue{
			Code: "capture_max_age_invalid", Message: "capture maximum age cannot be negative",
		})
	} else if maximumAge == 0 && captureHealthRequired(options) {
		maximumAge = time.Duration(2*config.IntervalSeconds+config.SourceTimeoutSeconds) * time.Second
	}
	if state.ActiveRunID != "" {
		status.IntegrityReady = false
		status.Issues = append(status.Issues, Issue{
			Code: "capture_run_incomplete", Message: "the latest capture run has no terminal audit event",
			RecoverBy: "verify the process stopped, then run explicit capture recovery",
		})
	}
	if state.LastRunOutcome == "abandoned" {
		status.Issues = append(status.Issues, Issue{
			Code:    "capture_last_run_abandoned",
			Message: "the latest capture run was abandoned and cannot establish current readiness",
		})
	}
	now := options.Now().UTC()
	for _, source := range config.Sources {
		entry, exists := state.Sources[source.ID]
		if !exists {
			entry = SourceStatus{
				SourceID: source.ID, Agent: source.Agent, Kind: source.Kind, Required: source.Required,
			}
		}
		entry.SourceID, entry.Agent, entry.Kind, entry.Required =
			source.ID, source.Agent, source.Kind, source.Required
		status.Sources = append(status.Sources, entry)
		if !source.Required || !sourceSelected(source, requiredAgents) {
			continue
		}
		if entry.LastRunID == "" || entry.ConfigSHA256 != state.ConfigSHA256 {
			status.Issues = append(status.Issues, Issue{
				Code: "capture_source_never_attempted", SourceID: source.ID,
				Message: "a required source has not been attempted under the active configuration",
			})
		} else if entry.LastRunID != state.LastRunID {
			status.Issues = append(status.Issues, Issue{
				Code: "capture_source_not_in_latest_run", SourceID: source.ID,
				Message: "a required source has no terminal result in the latest capture run",
			})
		} else if entry.LastOutcome != "success" {
			status.Issues = append(status.Issues, Issue{
				Code: "capture_latest_required_source_failed", SourceID: source.ID,
				Message: "the latest required source result is not a complete success",
			})
		}
		if entry.LastSuccessAt == nil || entry.ConfigSHA256 != state.ConfigSHA256 {
			status.Issues = append(status.Issues, Issue{
				Code: "capture_agent_never_succeeded", SourceID: source.ID,
				Message: "a required agent source has never completed successfully under the active configuration",
			})
			continue
		}
		if entry.LastSuccessAt.After(now) {
			status.Issues = append(status.Issues, Issue{
				Code: "capture_clock_anomaly", SourceID: source.ID,
				Message: "a capture success timestamp is in the future",
			})
		} else if maximumAge > 0 && now.Sub(*entry.LastSuccessAt) > maximumAge {
			status.Issues = append(status.Issues, Issue{
				Code: "capture_agent_stale", SourceID: source.ID,
				Message: "a required agent source is older than the allowed capture age",
			})
		}
	}
	for _, agent := range requiredAgents {
		configured := false
		for _, source := range config.Sources {
			if source.Required && source.Agent == agent {
				configured = true
				break
			}
		}
		if !configured {
			status.Issues = append(status.Issues, Issue{
				Code:    "capture_agent_not_configured",
				Message: "a required agent has no required capture source",
			})
		}
	}
	status.CaptureReady = status.Configured && !status.RunIncomplete && len(status.Issues) == 0
	status.Ready = status.IntegrityReady && (!captureHealthRequired(options) || status.CaptureReady)
	return finalizeStatus(status)
}

func finalizeStatus(status Status) Status {
	sort.Slice(status.Sources, func(left, right int) bool {
		return status.Sources[left].SourceID < status.Sources[right].SourceID
	})
	sortIssues := func(issues []Issue) {
		sort.Slice(issues, func(left, right int) bool {
			if issues[left].Code != issues[right].Code {
				return issues[left].Code < issues[right].Code
			}
			return issues[left].SourceID < issues[right].SourceID
		})
	}
	sortIssues(status.Issues)
	sortIssues(status.Warnings)
	return status
}

func captureHealthRequired(options StatusOptions) bool {
	return options.RequireHealthy || len(options.RequiredAgents) > 0 || options.MaximumAge != 0
}

func sourceSelected(source Source, agents []ledger.Agent) bool {
	if len(agents) == 0 {
		return true
	}
	for _, agent := range agents {
		if source.Agent == agent {
			return true
		}
	}
	return false
}

func uniqueAgents(agents []ledger.Agent) []ledger.Agent {
	result := []ledger.Agent{}
	for _, agent := range agents {
		result = appendAgent(result, agent)
	}
	return result
}

func appendAgent(agents []ledger.Agent, candidate ledger.Agent) []ledger.Agent {
	if candidate != ledger.AgentCodex && candidate != ledger.AgentClaudeCode &&
		candidate != ledger.AgentOpenCode {
		return agents
	}
	for _, agent := range agents {
		if agent == candidate {
			return agents
		}
	}
	return append(agents, candidate)
}
