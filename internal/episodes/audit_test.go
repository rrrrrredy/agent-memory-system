package episodes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestRecordGenerationAuditRejectsAdvancedLedgerPrefix(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	firstPayload := ledger.InlinePayload("utf-8", "text/plain", "first")
	first, err := store.Append(ledger.Event{SchemaVersion: ledger.SchemaVersion,
		EventID: "episode-audit-source", Kind: ledger.KindUserMessage,
		ObservedAt: now, RecordedAt: now,
		Source: ledger.Source{Agent: ledger.AgentCodex, Adapter: "test", AdapterVersion: "test/v1",
			DeviceID: store.DeviceID(), ThreadID: "audit-thread"},
		Payload: &firstPayload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"}})
	if err != nil {
		t.Fatal(err)
	}
	generationPath := filepath.Join(t.TempDir(), "generation")
	if err := os.MkdirAll(generationPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generationPath, "manifest.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := BuildResult{SchemaVersion: BuildSchemaVersion, DerivationVersion: DerivationVersion,
		SourceRecords: 1, SourceLastRecordHash: first.RecordHash, GenerationPath: generationPath}

	latePayload := ledger.InlinePayload("utf-8", "text/plain", "late correction")
	if _, err := store.Append(ledger.Event{SchemaVersion: ledger.SchemaVersion,
		EventID: "episode-audit-late-event", Kind: ledger.KindUserMessage,
		ObservedAt: now.Add(time.Second), RecordedAt: now.Add(time.Second),
		Source: ledger.Source{Agent: ledger.AgentCodex, Adapter: "test", AdapterVersion: "test/v1",
			DeviceID: store.DeviceID(), ThreadID: "audit-thread"},
		Payload: &latePayload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"}}); err != nil {
		t.Fatal(err)
	}

	if err := recordGenerationAudit(store, stale); err == nil ||
		!strings.Contains(err.Error(), "became stale before its completion audit") {
		t.Fatalf("advanced ledger prefix received a completion audit: %v", err)
	}
	audits, err := ListVerifiedGenerationAudits(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 0 {
		t.Fatalf("advanced ledger prefix left a reusable completion audit: %+v", audits)
	}
}
