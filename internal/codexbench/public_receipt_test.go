package codexbench

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublishedPublicReceiptSelfHash(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "evals", "results", "codex-memory-capability-v1-2026-08-12.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt PublicReceipt
	if err := decodeStrict(data, &receipt); err != nil {
		t.Fatal(err)
	}
	digest, err := publicReceiptSHA256(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.SchemaVersion != PublicReceiptSchema || digest != receipt.ReceiptSHA256 {
		t.Fatalf("published receipt self-hash is invalid: got %s want %s", digest, receipt.ReceiptSHA256)
	}
}
