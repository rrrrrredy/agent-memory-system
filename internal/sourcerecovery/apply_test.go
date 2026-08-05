package sourcerecovery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/adapters/codex"
	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestApplyRecoversRelocatedSourceAndRecordsStableGap(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := t.TempDir()
	threadID := "00000000-0000-0000-0000-000000000041"
	relative := "archive/rollout-2027-02-01T08-00-00-" + threadID + ".jsonl"
	path := filepath.Join(sourceRoot, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(
		`{"timestamp":"2027-02-01T08:00:00Z","type":"session_meta","payload":{"id":"` + threadID + `"}}` + "\n" +
			`{"timestamp":"2027-02-01T08:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":"keep the original evidence"}}` + "\n",
	)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	contentDigest := sha256.Sum256(data)
	bytesExpected := int64(len(data))
	logicalHash := strings.Repeat("1", 64)
	missingHash := strings.Repeat("2", 64)
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion,
		CreatedAt:     time.Date(2027, 2, 1, 9, 0, 0, 0, time.UTC),
		Agent:         ledger.AgentCodex,
		Entries: []Entry{
			{
				LogicalSourcePathSHA256: logicalHash, ThreadID: threadID,
				Status: StatusAvailable, RelativePath: relative,
				ExpectedContentSHA256: hex.EncodeToString(contentDigest[:]), ExpectedBytes: &bytesExpected,
			},
			{
				LogicalSourcePathSHA256: missingHash,
				ThreadID:                "00000000-0000-0000-0000-000000000042",
				Status:                  StatusMissing, Reason: "source_not_found_after_local_search",
			},
		},
		Privacy: "local_only",
	}
	raw := encodeManifest(t, &manifest)
	now := time.Date(2027, 2, 1, 10, 0, 0, 0, time.UTC)
	result, err := Apply(store, sourceRoot, bytes.NewReader(raw), now)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourcesRecovered != 1 || result.SourcesMissing != 1 || result.GapsAppended != 1 ||
		result.ManifestReused || result.Import == nil || result.Import.SourceSegments != 1 {
		t.Fatalf("unexpected recovery result: %+v", result)
	}

	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	acquisitionHash, err := adapterjsonl.HashSourcePath(resolvedPath)
	if err != nil {
		t.Fatal(err)
	}
	var recovered, gap, manifestSnapshot int
	err = store.VisitRecords(func(record ledger.Record) error {
		event := record.Event
		switch {
		case event.Source.Adapter == codex.AdapterName:
			recovered++
			if event.Source.SourcePathHash != logicalHash || event.Source.AcquisitionPathHash != acquisitionHash {
				t.Fatalf("relocated identity was not preserved: %+v", event.Source)
			}
			if event.Kind == ledger.KindSourceSnapshot {
				file, openErr := store.OpenBlob(*event.Payload.Blob)
				if openErr != nil {
					return openErr
				}
				preserved, readErr := os.ReadFile(file.Name())
				_ = file.Close()
				if readErr != nil {
					return readErr
				}
				if !bytes.Equal(preserved, data) {
					t.Fatal("recovered source bytes changed")
				}
			}
		case event.Source.Adapter == AdapterName && event.Kind == ledger.KindSourceSnapshot:
			manifestSnapshot++
		case event.Source.Adapter == AdapterName && event.Kind == ledger.KindGap:
			gap++
			if event.Source.SourcePathHash != missingHash || event.Completeness.Status != ledger.CompletenessMissing {
				t.Fatalf("missing source gap is not evidence-bound: %+v", event)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 3 || gap != 1 || manifestSnapshot != 1 {
		t.Fatalf("unexpected recovered evidence counts: recovered=%d gap=%d manifest=%d", recovered, gap, manifestSnapshot)
	}

	repeated, err := Apply(store, sourceRoot, bytes.NewReader(raw), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.ManifestReused || repeated.GapsReused != 1 || repeated.GapsAppended != 0 {
		t.Fatalf("reapplying recovery was not idempotent: %+v", repeated)
	}
}

func TestApplyRejectsContentMismatchBeforeAppendingEvidence(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := t.TempDir()
	threadID := "00000000-0000-0000-0000-000000000043"
	relative := "rollout-2027-02-01T08-00-00-" + threadID + ".jsonl"
	data := []byte(`{"timestamp":"2027-02-01T08:00:00Z","type":"session_meta","payload":{"id":"` + threadID + `"}}` + "\n")
	if err := os.WriteFile(filepath.Join(sourceRoot, relative), data, 0o600); err != nil {
		t.Fatal(err)
	}
	bytesExpected := int64(len(data))
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, CreatedAt: time.Unix(4000, 0).UTC(),
		Agent: ledger.AgentCodex,
		Entries: []Entry{{
			LogicalSourcePathSHA256: strings.Repeat("3", 64), ThreadID: threadID,
			Status: StatusAvailable, RelativePath: relative,
			ExpectedContentSHA256: strings.Repeat("0", 64), ExpectedBytes: &bytesExpected,
		}},
		Privacy: "local_only",
	}
	_, err = Apply(store, sourceRoot, bytes.NewReader(encodeManifest(t, &manifest)), time.Unix(4001, 0))
	if err == nil || !strings.Contains(err.Error(), "content digest") {
		t.Fatalf("content mismatch was accepted: %v", err)
	}
	if report := store.Verify(); report.RecordsChecked != 0 || len(report.Issues) != 0 {
		t.Fatalf("failed recovery appended ledger records: %+v", report)
	}
}

func TestApplyRejectsGitContainedSourceRoot(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(sourceRoot, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, CreatedAt: time.Unix(5000, 0).UTC(),
		Agent: ledger.AgentCodex,
		Entries: []Entry{{
			LogicalSourcePathSHA256: strings.Repeat("4", 64),
			ThreadID:                "00000000-0000-0000-0000-000000000044",
			Status:                  StatusMissing, Reason: "source_not_found_after_local_search",
		}},
		Privacy: "local_only",
	}
	_, err = Apply(store, sourceRoot, bytes.NewReader(encodeManifest(t, &manifest)), time.Unix(5001, 0))
	if err == nil || !strings.Contains(err.Error(), "outside every Git worktree") {
		t.Fatalf("Git-contained source root was accepted: %v", err)
	}
}

func TestOpenManifestFileRejectsGitContainedPath(t *testing.T) {
	repository := t.TempDir()
	if err := os.Mkdir(filepath.Join(repository, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repository, "recovery.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenManifestFile(path); err == nil || !strings.Contains(err.Error(), "outside every Git worktree") {
		t.Fatalf("Git-contained recovery manifest was accepted: %v", err)
	}
}

func TestApplyKeepsDistinctMissingSourceRecoveryAttempts(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, CreatedAt: time.Unix(5500, 0).UTC(),
		Agent: ledger.AgentCodex,
		Entries: []Entry{{
			LogicalSourcePathSHA256: strings.Repeat("9", 64),
			ThreadID:                "00000000-0000-0000-0000-000000000049",
			Status:                  StatusMissing, Reason: "source_not_found_after_local_search",
		}},
		Privacy: "local_only",
	}
	first, err := Apply(store, t.TempDir(), bytes.NewReader(encodeManifest(t, &manifest)), time.Unix(5501, 0))
	if err != nil {
		t.Fatal(err)
	}
	manifest.CreatedAt = time.Unix(5600, 0).UTC()
	second, err := Apply(store, t.TempDir(), bytes.NewReader(encodeManifest(t, &manifest)), time.Unix(5601, 0))
	if err != nil {
		t.Fatal(err)
	}
	if first.GapsAppended != 1 || second.GapsAppended != 1 || second.GapsReused != 0 ||
		first.RecoveryID == second.RecoveryID {
		t.Fatalf("distinct recovery attempts were conflated: first=%+v second=%+v", first, second)
	}
}

func TestDecodeManifestRejectsUnknownAndTrailingData(t *testing.T) {
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, CreatedAt: time.Unix(6000, 0).UTC(),
		Agent: ledger.AgentCodex,
		Entries: []Entry{{
			LogicalSourcePathSHA256: strings.Repeat("5", 64), ThreadID: "thread-5",
			Status: StatusMissing, Reason: "source_not_found",
		}},
		Privacy: "local_only",
	}
	raw := encodeManifest(t, &manifest)
	unknown := bytes.Replace(raw, []byte(`"privacy"`), []byte(`"unknown":true,"privacy"`), 1)
	if _, _, err := DecodeManifest(bytes.NewReader(unknown)); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown manifest field was accepted: %v", err)
	}
	if _, _, err := DecodeManifest(bytes.NewReader(append(raw, []byte("{}")...))); err == nil ||
		!strings.Contains(err.Error(), "more than one") {
		t.Fatalf("trailing manifest value was accepted: %v", err)
	}
}

func encodeManifest(t *testing.T, manifest *Manifest) []byte {
	t.Helper()
	id, err := RecoveryID(*manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.RecoveryID = id
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}
