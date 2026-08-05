package diagnostics

import (
	"context"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/capturesupervisor"
	"github.com/rrrrrredy/agent-memory-system/internal/gitsync"
	"github.com/rrrrrredy/agent-memory-system/internal/hookcapture"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/promotion"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
	"github.com/rrrrrredy/agent-memory-system/internal/ruleapproval"
)

const SchemaVersion = "agent-memory-doctor-report/v1alpha1"

type Issue struct {
	Component string `json:"component"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

type Options struct {
	Repository            string
	RequireRepository     bool
	RequireCapture        bool
	CaptureRequiredAgents []ledger.Agent
	CaptureMaximumAge     time.Duration
	Now                   func() time.Time
}

type Report struct {
	SchemaVersion     string                              `json:"schema_version"`
	CheckedAt         time.Time                           `json:"checked_at"`
	Platform          string                              `json:"platform"`
	Evidence          ledger.VerificationReport           `json:"evidence"`
	HookSpools        hookcapture.SpoolVerificationReport `json:"hook_spools"`
	Reviews           review.VerificationReport           `json:"reviews"`
	Promotions        promotion.VerificationReport        `json:"promotions"`
	RuleApprovals     ruleapproval.VerificationReport     `json:"rule_approvals"`
	Retrieval         retrieval.VerificationReport        `json:"retrieval"`
	CaptureSupervisor *capturesupervisor.Status           `json:"capture_supervisor,omitempty"`
	RepositoryChecked bool                                `json:"repository_checked"`
	GitSync           *gitsync.VerificationReport         `json:"git_sync,omitempty"`
	Ready             bool                                `json:"ready"`
	Issues            []Issue                             `json:"issues"`
	Privacy           string                              `json:"privacy"`
}

func Run(ctx context.Context, store *ledger.Store, options Options) Report {
	if options.Now == nil {
		options.Now = time.Now
	}
	report := Report{
		SchemaVersion: SchemaVersion, CheckedAt: options.Now().UTC(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Issues:   []Issue{}, Privacy: "local_only",
	}
	if store == nil {
		report.Issues = append(report.Issues, Issue{
			Component: "evidence", Code: "store_required", Message: "local evidence store is required",
		})
		return finalize(report)
	}
	report.Evidence = store.Verify()
	appendStrings(&report, "evidence", "integrity_failed", report.Evidence.Issues)
	report.HookSpools = hookcapture.VerifySpools(store)
	appendStrings(&report, "hook_spools", "integrity_failed", report.HookSpools.Issues)
	report.Reviews = review.Verify(store)
	appendStrings(&report, "reviews", "integrity_failed", report.Reviews.Issues)
	report.Promotions = promotion.Verify(store)
	appendStrings(&report, "promotions", "integrity_failed", report.Promotions.Issues)
	report.RuleApprovals = ruleapproval.Verify(store)
	appendStrings(&report, "rule_approvals", "integrity_failed", report.RuleApprovals.Issues)
	report.Retrieval = retrieval.Verify(store)
	appendStrings(&report, "retrieval", "integrity_failed", report.Retrieval.Issues)
	strictCapture := options.RequireCapture || len(options.CaptureRequiredAgents) > 0 ||
		options.CaptureMaximumAge != 0
	captureStatus, _ := capturesupervisor.GetStatus(store, capturesupervisor.StatusOptions{
		RequireConfigured: options.RequireCapture, RequireHealthy: options.RequireCapture ||
			len(options.CaptureRequiredAgents) > 0 || options.CaptureMaximumAge != 0,
		RequiredAgents: options.CaptureRequiredAgents, MaximumAge: options.CaptureMaximumAge,
		Now: options.Now,
	})
	if captureStatus.Configured || options.RequireCapture || len(captureStatus.Issues) > 0 {
		report.CaptureSupervisor = &captureStatus
		if strictCapture {
			for _, issue := range captureStatus.Issues {
				report.Issues = append(report.Issues, Issue{
					Component: "capture_supervisor", Code: issue.Code, Message: issue.Message,
				})
			}
		} else if !captureStatus.IntegrityReady {
			report.Issues = append(report.Issues, Issue{
				Component: "capture_supervisor", Code: "integrity_failed",
				Message: "capture supervisor append-only state failed integrity verification",
			})
		}
	}

	if strings.TrimSpace(options.Repository) == "" {
		if options.RequireRepository {
			report.Issues = append(report.Issues, Issue{
				Component: "git_sync", Code: "repository_required",
				Message: "portable memory repository is required for a complete recovery check",
			})
		}
		return finalize(report)
	}
	report.RepositoryChecked = true
	if storageZonesOverlap(store.Root(), options.Repository) {
		report.Issues = append(report.Issues, Issue{
			Component: "storage", Code: "storage_zones_overlap",
			Message: "local evidence and portable memory repositories must be separate",
		})
		return finalize(report)
	}
	gitReport := gitsync.Verify(ctx, options.Repository)
	report.GitSync = &gitReport
	for _, issue := range gitReport.Issues {
		report.Issues = append(report.Issues, Issue{
			Component: "git_sync", Code: issue.Code, Message: issue.Message,
		})
	}
	return finalize(report)
}

func appendStrings(report *Report, component, code string, issues []string) {
	for _, message := range issues {
		report.Issues = append(report.Issues, Issue{
			Component: component, Code: code, Message: message,
		})
	}
}

func finalize(report Report) Report {
	sort.Slice(report.Issues, func(left, right int) bool {
		if report.Issues[left].Component != report.Issues[right].Component {
			return report.Issues[left].Component < report.Issues[right].Component
		}
		if report.Issues[left].Code != report.Issues[right].Code {
			return report.Issues[left].Code < report.Issues[right].Code
		}
		return report.Issues[left].Message < report.Issues[right].Message
	})
	report.Ready = len(report.Issues) == 0
	return report
}

func storageZonesOverlap(left, right string) bool {
	left, leftErr := filepath.Abs(left)
	right, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return true
	}
	if resolved, err := filepath.EvalSymlinks(left); err == nil {
		left = resolved
	}
	if resolved, err := filepath.EvalSymlinks(right); err == nil {
		right = resolved
	}
	return within(left, right) || within(right, left)
}

func within(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
