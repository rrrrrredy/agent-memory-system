package schemas_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/hookcapture"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestCaptureInstancesMatchPublishedSchemas(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	rollout := filepath.Join(source,
		"rollout-2027-01-15T08-00-00-00000000-0000-0000-0000-000000000003.jsonl")
	data := []byte(`{"timestamp":"2027-01-15T08:00:00Z","type":"session_meta","payload":{"id":"00000000-0000-0000-0000-000000000003"}}` + "\n")
	if err := os.WriteFile(rollout, data, 0o600); err != nil {
		t.Fatal(err)
	}
	raw := `{"session_id":"00000000-0000-0000-0000-000000000003","transcript_path":` +
		quoteJSONString(rollout) + `,"hook_event_name":"SessionEnd"}`
	captured, err := hookcapture.Capture(store, ledger.AgentCodex, strings.NewReader(raw),
		hookcapture.CaptureOptions{Now: func() time.Time { return time.Unix(3000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	envelopeData, err := os.ReadFile(filepath.Join(store.Root(), filepath.FromSlash(captured.Relative)))
	if err != nil {
		t.Fatal(err)
	}
	envelope, _, err := hookcapture.DecodeEnvelope(bytes.TrimSpace(envelopeData))
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := hookcapture.Reconcile(store, ledger.AgentCodex,
		hookcapture.ReconcileOptions{SourcePath: source, Now: func() time.Time { return time.Unix(3001, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	validatePublishedInstance(t, "agent-hook-envelope.schema.json", envelope)
	validatePublishedInstance(t, "capture-reconciliation.schema.json", reconciled)
}

func quoteJSONString(value string) string {
	var buffer bytes.Buffer
	buffer.WriteByte('"')
	for _, character := range value {
		switch character {
		case '\\', '"':
			buffer.WriteByte('\\')
			buffer.WriteRune(character)
		default:
			buffer.WriteRune(character)
		}
	}
	buffer.WriteByte('"')
	return buffer.String()
}
