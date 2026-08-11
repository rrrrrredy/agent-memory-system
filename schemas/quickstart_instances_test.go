package schemas_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/adapters/opencode"
	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/promotion"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
	"github.com/rrrrrredy/agent-memory-system/internal/secretscan"
)

func TestQuickstartResultInstancesMatchPublishedSchemas(t *testing.T) {
	hashA, hashB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	hashC, hashD := strings.Repeat("c", 64), strings.Repeat("d", 64)
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	imported := adapterjsonl.Result{
		SchemaVersion: adapterjsonl.ResultSchemaVersion, FilesExamined: 1, FilesChanged: 1,
		SourceSegments: 2, EventsAppended: 4, BytesCaptured: 128,
		Kinds: map[string]int{"user_message": 1},
	}
	episodeBuild := episodes.BuildResult{
		SchemaVersion: episodes.BuildSchemaVersion, DerivationVersion: episodes.DerivationVersion,
		SourceRecords: 5, SourceLastRecordHash: hashA, GenerationPath: "episodes-v1alpha1-example",
		Episodes: 1, TimelineEntries: 4, Compactions: 0, TimelineSHA256: hashB,
		EpisodesSHA256: hashC, Reused: false,
	}
	candidateBuild := candidates.BuildResult{
		SchemaVersion: candidates.BuildSchemaVersion, DerivationVersion: candidates.DerivationVersion,
		SourceEpisodeGeneration: "episodes-v1alpha1-example", SourceEpisodeManifestSHA256: hashA,
		SourceEpisodesSHA256: hashB, SourceEpisodes: 1, GenerationPath: "candidates-v1alpha1-example",
		Observations: 1, Candidates: 1, ReviewReady: 1, CandidatesSHA256: hashC,
	}
	transition := review.Transition{
		CandidateID: "candidate-" + hashA, CandidateContentSHA256: hashB,
		ExpectedStatus: review.StatusPending, Action: review.ActionValidate,
		ResultingStatus: review.StatusValidated,
		Scope:           &review.Scope{Kind: review.ScopeProject, Value: "example-project"},
		Basis:           []review.Basis{review.BasisExplicitRemember}, Reason: "Reviewed source evidence.",
	}
	reviewApply := review.ApplyResult{
		SchemaVersion: review.ApplySchemaVersion, EventID: "review-example", Sequence: 1,
		RecordSHA256: hashD, Transitions: []review.Transition{transition}, Privacy: "local_only",
	}
	revision := promotion.Revision{
		SchemaVersion: promotion.RevisionSchemaVersion, MemoryID: "memory-" + hashA,
		RevisionID: "memory-revision-" + hashB, Action: promotion.ActionPromote,
		Status: promotion.StatusActive, Kind: candidates.KindDirective, Text: "Keep reports local.",
		TextSHA256: hashC, Scope: review.Scope{Kind: review.ScopeProject, Value: "example-project"},
		Source: &promotion.Source{
			CandidateGeneration: "candidates-v1alpha1-example", CandidatesSHA256: hashA,
			CandidateID: "candidate-" + hashB, CandidateContentSHA256: hashC,
			SemanticKeySHA256: hashD, ReviewEventID: "review-example",
			ReviewRecordSHA256: hashA, ReviewScope: review.Scope{Kind: review.ScopeProject, Value: "example-project"},
			ReviewBasis: []review.Basis{review.BasisExplicitRemember},
		},
		Scan: &promotion.ScanAttestation{
			ScannerVersion: secretscan.ScannerVersion, SourceTextSHA256: hashC,
			FindingIDs: []string{}, Redactions: []secretscan.Redaction{}, RedactedTextSHA256: hashC,
		},
		RuleChangeAuthorization: "not_granted", RecordedAt: now, OriginDeviceID: "device-example",
		ApproverID: "owner", Reason: "Approved for portable memory.", Privacy: "local_only",
	}
	promotionApply := promotion.ApplyResult{
		SchemaVersion: promotion.ApplyResultSchemaVersion, EventID: "promotion-example",
		Sequence: 1, RecordSHA256: hashD, Revision: revision, Privacy: "local_only",
	}
	portableInit := portable.InitResult{
		SchemaVersion: portable.InitResultSchemaVersion, Initialized: true,
		Privacy: portable.PortablePrivacy,
	}
	portableExport := portable.ExportResult{
		SchemaVersion: portable.ExportResultSchemaVersion, MemoriesSelected: 1,
		RevisionsProjected: 1, RevisionsWritten: 1, FilesWritten: []string{"memories/example.md"},
		Privacy: portable.PortablePrivacy,
	}
	portableVerification := portable.VerificationReport{
		SchemaVersion: portable.VerificationSchemaVersion, FilesChecked: 2,
		RevisionsChecked: 1, MemoriesChecked: 1, ActiveMemories: 1,
		Issues: []portable.VerificationIssue{}, Privacy: portable.PortablePrivacy,
	}
	instances := []struct {
		schema string
		value  any
	}{
		{"agent-history-import-result.schema.json", imported},
		{"episode-build-result.schema.json", episodeBuild},
		{"candidate-build-result.schema.json", candidateBuild},
		{"candidate-review-apply-result.schema.json", reviewApply},
		{"memory-promotion-apply-result.schema.json", promotionApply},
		{"portable-memory-init-result.schema.json", portableInit},
		{"portable-memory-export-result.schema.json", portableExport},
		{"portable-memory-verification.schema.json", portableVerification},
	}
	for _, instance := range instances {
		t.Run(instance.schema, func(t *testing.T) {
			validatePublishedInstance(t, instance.schema, instance.value)
			data, err := json.Marshal(instance.value)
			if err != nil {
				t.Fatal(err)
			}
			var wrongVersion map[string]any
			if err := json.Unmarshal(data, &wrongVersion); err != nil {
				t.Fatal(err)
			}
			wrongVersion["schema_version"] = "unsupported/v1"
			rejectPublishedInstance(t, instance.schema, wrongVersion)
		})
	}
}

func TestAgentHistoryImportSchemaCoversOpenCodeSnapshots(t *testing.T) {
	instance := opencode.Result{
		SchemaVersion: adapterjsonl.ResultSchemaVersion,
		FilesExamined: 1, FilesChanged: 1, SourceSnapshots: 1,
		EventsAppended: 2, BytesCaptured: 256,
		Kinds: map[string]int{"user_message": 1, "assistant_message": 1},
	}
	validatePublishedInstance(t, "agent-history-import-result.schema.json", instance)

	data, err := json.Marshal(instance)
	if err != nil {
		t.Fatal(err)
	}
	var invalid map[string]any
	if err := json.Unmarshal(data, &invalid); err != nil {
		t.Fatal(err)
	}
	invalid["source_segments"] = 1
	rejectPublishedInstance(t, "agent-history-import-result.schema.json", invalid)
	delete(invalid, "source_segments")
	delete(invalid, "source_snapshots")
	rejectPublishedInstance(t, "agent-history-import-result.schema.json", invalid)
}
