package episodes

import (
	"testing"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestExtractEventTextUsesDeterministicObjectOrder(t *testing.T) {
	raw := []byte(`{"text":"third","message":"second","content":"first"}`)
	const want = "first\nsecond\nthird"
	for attempt := 0; attempt < 100; attempt++ {
		if got := extractEventText(ledger.AgentCodex, ledger.KindUserMessage, raw); got != want {
			t.Fatalf("attempt %d: got %q, want %q", attempt, got, want)
		}
	}
}

func TestRawResolverSharesGlobalSourceSnapshotIndex(t *testing.T) {
	local := ledger.Event{EventID: "local"}
	snapshots := map[string]ledger.Event{}
	resolver := newRawResolver(nil, []ledger.Event{local}, snapshots)

	snapshots["parent"] = ledger.Event{EventID: "parent"}
	if len(resolver.events) != 1 {
		t.Fatalf("resolver copied global snapshots into its local index: %d entries", len(resolver.events))
	}
	if parent, ok := resolver.eventByID("parent"); !ok || parent.EventID != "parent" {
		t.Fatal("resolver does not share the global source snapshot index")
	}
}

func TestStatementMarkersUseNormalizedWordBoundaries(t *testing.T) {
	if got := classifyNormalizedStatement(normalizeText("The address changed"), false); got != "" {
		t.Fatalf("substring marker produced %q", got)
	}
	if got := classifyNormalizedStatement(normalizeText("Don't upload raw evidence"), false); got != "constraint" {
		t.Fatalf("normalized apostrophe marker produced %q", got)
	}
	if got := classifyNormalizedStatement(normalizeText("I said again: keep offline mode"), false); got != "correction" {
		t.Fatalf("correction phrase produced %q", got)
	}
}
