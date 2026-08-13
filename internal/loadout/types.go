package loadout

import (
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

const (
	ContextReceiptSchemaVersion = "memory-loadout-context-receipt/v1alpha1"
	ContextResultSchemaVersion  = "memory-loadout-context-result/v1alpha1"
	VerificationSchemaVersion   = "memory-loadout-context-verification/v1alpha1"
	AdapterVersion              = "v1alpha1"
	PrivacyLocalOnly            = "local_only"
)

type ContextReceipt struct {
	SchemaVersion       string                      `json:"schema_version"`
	ReceiptID           string                      `json:"receipt_id"`
	RecordedAt          time.Time                   `json:"recorded_at"`
	Loadout             portable.Loadout            `json:"loadout"`
	Context             retrieval.Context           `json:"context"`
	RetrievalReceiptIDs []string                    `json:"retrieval_receipt_ids"`
	Memories            []retrieval.MemoryReference `json:"memories"`
	Content             string                      `json:"content"`
	ContentSHA256       string                      `json:"content_sha256"`
	ContentBytes        int                         `json:"content_bytes"`
	EstimatedTokens     int                         `json:"estimated_tokens"`
	Privacy             string                      `json:"privacy"`
}

type ContextResult struct {
	SchemaVersion string         `json:"schema_version"`
	Receipt       ContextReceipt `json:"receipt"`
	Privacy       string         `json:"privacy"`
}

type VerificationReport struct {
	SchemaVersion   string   `json:"schema_version"`
	ReceiptsChecked int      `json:"receipts_checked"`
	Issues          []string `json:"issues"`
	Privacy         string   `json:"privacy"`
}
