package schemas_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishedCodexMemoryReceiptMatchesSchema(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "evals", "results", "codex-memory-capability-v1-2026-08-12.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	validatePublishedInstance(t, "codex-memory-benchmark-public-receipt.schema.json", receipt)
	if receipt["privacy"] != "synthetic_public_aggregate" {
		t.Fatal("public receipt changed its aggregate-only privacy boundary")
	}
}
