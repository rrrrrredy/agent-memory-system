package portable

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/promotion"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
	"github.com/rrrrrredy/agent-memory-system/internal/secretscan"
)

func TestPortableRevisionRoundTripOmitsLocalProof(t *testing.T) {
	revision := testRootRevision(t, "Keep reports local.")
	data, err := RenderRevision(revision)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"candidate_generation", "candidate_content_sha256", "review_record_sha256",
		"scanner_version", "source_sha256", "origin_device_id", "approver_id",
		"reason:", "local_only",
	} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatalf("portable revision retained local proof field %q", forbidden)
		}
	}
	parsed, err := ParseRevision(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parsed, revision) {
		t.Fatalf("round trip changed revision:\n got: %+v\nwant: %+v", parsed, revision)
	}
	tampered := bytes.Replace(data, []byte("reports"), []byte("secrets"), 1)
	if _, err := ParseRevision(tampered); err == nil ||
		!strings.Contains(err.Error(), "content") {
		t.Fatalf("tampered portable text was accepted: %v", err)
	}
}

func TestRepositoryVerificationDetectsForksAndSemanticConflicts(t *testing.T) {
	t.Run("fork", func(t *testing.T) {
		root := t.TempDir()
		if err := InitRepository(root); err != nil {
			t.Fatal(err)
		}
		initial := testRootRevision(t, "Keep reports local.")
		first := testChildRevision(t, initial, "Keep reports in local storage.")
		second := testChildRevision(t, initial, "Keep reports on this device.")
		for _, revision := range []Revision{initial, first, second} {
			writeTestRevision(t, root, revision)
		}
		report := VerifyRepository(root)
		if !hasIssue(report, "revision_fork") || !hasIssue(report, "head_conflict") {
			t.Fatalf("fork was not quarantined: %+v", report)
		}
	})

	t.Run("opposing semantics", func(t *testing.T) {
		root := t.TempDir()
		if err := InitRepository(root); err != nil {
			t.Fatal(err)
		}
		for _, revision := range []Revision{
			testRootRevision(t, "Keep reports local."),
			testRootRevision(t, "Do not keep reports local."),
		} {
			writeTestRevision(t, root, revision)
		}
		report := VerifyRepository(root)
		if !hasIssue(report, "semantic_conflict") {
			t.Fatalf("opposing memories were not quarantined: %+v", report)
		}
	})
}

func TestRepositoryVerificationRejectsUnknownFilesAndTampering(t *testing.T) {
	root := t.TempDir()
	if err := InitRepository(root); err != nil {
		t.Fatal(err)
	}
	revision := testRootRevision(t, "Keep reports local.")
	writeTestRevision(t, root, revision)
	if report := VerifyRepository(root); len(report.Issues) != 0 ||
		report.ActiveMemories != 1 || report.RevisionsChecked != 1 {
		t.Fatalf("clean repository did not verify: %+v", report)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("not allowlisted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if report := VerifyRepository(root); !hasIssue(report, "unexpected_path") {
		t.Fatalf("unknown repository file was accepted: %+v", report)
	}
}

func TestRepositoryVerificationAllowsGitMetadata(t *testing.T) {
	root := t.TempDir()
	if err := InitRepository(root); err != nil {
		t.Fatal(err)
	}
	gitDirectory := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDirectory, "config"), []byte("[core]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if report := VerifyRepository(root); len(report.Issues) != 0 {
		t.Fatalf("Git metadata changed repository verification: %+v", report)
	}
}

func TestProjectionUsesOnlyAllowlistedPromotedFields(t *testing.T) {
	text := "Keep reports local."
	scope := review.Scope{Kind: review.ScopeGlobal, Value: "*"}
	memoryID := expectedMemoryID(textDigest(text), scope.Kind, scope.Value)
	localRevisionID := "memory-revision-" + strings.Repeat("a", 64)
	local := promotion.Revision{
		SchemaVersion: promotion.RevisionSchemaVersion,
		MemoryID:      memoryID, RevisionID: localRevisionID,
		Action: promotion.ActionPromote, Status: promotion.StatusActive,
		Kind: candidates.KindDirective, Text: text, TextSHA256: textDigest(text), Scope: scope,
		Source: &promotion.Source{
			CandidateGeneration: "local-generation-proof",
			CandidatesSHA256:    strings.Repeat("b", 64), CandidateID: "candidate-" + strings.Repeat("c", 64),
			CandidateContentSHA256: strings.Repeat("d", 64), SemanticKeySHA256: strings.Repeat("e", 64),
			ReviewEventID: "review-local-proof", ReviewRecordSHA256: strings.Repeat("f", 64),
			ReviewScope: scope, ReviewBasis: []review.Basis{review.BasisExplicitRemember},
		},
		Scan: &promotion.ScanAttestation{
			ScannerVersion: secretscan.ScannerVersion, SourceTextSHA256: strings.Repeat("1", 64),
			FindingIDs: []string{"finding-" + strings.Repeat("2", 64)},
			Redactions: []secretscan.Redaction{{
				StartByte: 1, EndByte: 2, SourceSHA256: strings.Repeat("3", 64),
				Replacement: "[REDACTED:secret]",
			}},
			RedactedTextSHA256: textDigest(text),
		},
		RequiresExplicitRuleChangeApproval: true,
		RuleChangeAuthorization:            "not_granted", OriginDeviceID: "local-device-proof",
		ApproverID: "local-approver-proof", Reason: "local reason proof", Privacy: "local_only",
	}
	projected, err := projectHistory(promotion.History{
		MemoryID: memoryID, Revisions: []promotion.Revision{local},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(projected) != 1 || projected[0].Text != text ||
		!projected[0].RequiresExplicitRuleChangeApproval {
		t.Fatalf("unexpected projection: %+v", projected)
	}
	data, err := RenderRevision(projected[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"local-generation-proof", "review-local-proof", "local-device-proof",
		"local-approver-proof", "local reason proof", strings.Repeat("3", 64),
	} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatalf("projection leaked local proof %q", forbidden)
		}
	}
}

func TestSeparateRootCheckRejectsNestedStorage(t *testing.T) {
	evidence := t.TempDir()
	repository := filepath.Join(evidence, "private-memory")
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ensureSeparateRoots(evidence, repository); err == nil {
		t.Fatal("nested portable repository was accepted")
	}
}

func testRootRevision(t *testing.T, text string) Revision {
	t.Helper()
	scopeKind, scopeValue := review.ScopeGlobal, "*"
	revision, err := finalizeRevision(Revision{
		MemoryID: expectedMemoryID(textDigest(text), scopeKind, scopeValue),
		Action:   ActionPromote, Status: StatusActive, Kind: candidates.KindDirective,
		ScopeKind: scopeKind, ScopeValue: scopeValue,
		EvidenceBasis: []review.Basis{review.BasisExplicitRemember}, Text: text,
	})
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func testChildRevision(t *testing.T, parent Revision, text string) Revision {
	t.Helper()
	revision, err := finalizeRevision(Revision{
		MemoryID: parent.MemoryID, ParentRevisionID: parent.RevisionID,
		Action: ActionSupersede, Status: StatusActive, Kind: candidates.KindCorrection,
		ScopeKind: parent.ScopeKind, ScopeValue: parent.ScopeValue,
		EvidenceBasis: []review.Basis{review.BasisUserCorrection}, Text: text,
	})
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func writeTestRevision(t *testing.T, root string, revision Revision) {
	t.Helper()
	data, err := RenderRevision(revision)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, revisionRelativePath(revision))
	if err := writeFileAtomic(path, data); err != nil {
		t.Fatal(err)
	}
}

func hasIssue(report VerificationReport, code string) bool {
	for _, issue := range report.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
