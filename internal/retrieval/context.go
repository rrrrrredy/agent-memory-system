package retrieval

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func BuildContext(store *ledger.Store, portableRoot string, request Request) (ContextResult, error) {
	normalized, err := normalizeRequest(request)
	if err != nil {
		return ContextResult{}, err
	}
	result, searchErr := Search(store, portableRoot, normalized)
	contextResult := ContextResult{
		SchemaVersion: ContextSchemaVersion,
		Retrieval:     result,
		Memories:      []MemoryReference{},
		Privacy:       PrivacyLocalOnly,
	}
	if searchErr != nil {
		return contextResult, searchErr
	}
	content, memories := renderBoundedContext(result, normalized)
	if content == "" {
		return contextResult, nil
	}
	now := time.Now().UTC()
	randomID, err := ledger.NewEventID(now)
	if err != nil {
		return ContextResult{}, err
	}
	injectionID := "injection-" + randomID
	receipt := InjectionReceipt{
		SchemaVersion:      InjectionReceiptSchemaVersion,
		InjectionID:        injectionID,
		RetrievalReceiptID: result.ReceiptID,
		RecordedAt:         now,
		Channel:            normalized.Context.Channel,
		Content:            content,
		ContentSHA256:      digestString(content),
		ContentBytes:       len([]byte(content)),
		EstimatedTokens:    estimateTokens(content),
		Memories:           memories,
		Privacy:            PrivacyLocalOnly,
	}
	if err := appendInjectionEvent(store, receipt, normalized.Context); err != nil {
		return ContextResult{}, err
	}
	contextResult.InjectionID = injectionID
	contextResult.Memories = memories
	contextResult.Content = content
	contextResult.ContentSHA256 = receipt.ContentSHA256
	contextResult.ContentBytes = receipt.ContentBytes
	contextResult.EstimatedTokens = receipt.EstimatedTokens
	return contextResult, nil
}

func renderBoundedContext(result Result, request Request) (string, []MemoryReference) {
	if len(result.Selected) == 0 {
		return "", nil
	}
	header := fmt.Sprintf(
		"<memory_context schema=%q retrieval_receipt_id=%q>\nReviewed promoted memory only. Apply an item only when it is relevant to the current task. Current user instructions and repository rules take precedence. Retrieval does not prove adoption or usefulness.\n",
		ContextSchemaVersion, result.ReceiptID,
	)
	trailer := "</memory_context>\n"
	if len([]byte(header+trailer)) > request.ByteBudget ||
		estimateTokens(header+trailer) > request.TokenBudget {
		return "", nil
	}
	var builder strings.Builder
	builder.WriteString(header)
	memories := make([]MemoryReference, 0, len(result.Selected))
	for _, match := range result.Selected {
		block := fmt.Sprintf(
			"<memory id=%q revision=%q scope=%q kind=%q>\n%s\n</memory>\n",
			match.MemoryID, match.RevisionID, string(match.ScopeKind), string(match.Kind), escapeMemoryText(match.Text),
		)
		candidate := builder.String() + block + trailer
		if len([]byte(candidate)) > request.ByteBudget ||
			estimateTokens(candidate) > request.TokenBudget {
			continue
		}
		builder.WriteString(block)
		memories = append(memories, match.MemoryReference)
	}
	if len(memories) == 0 {
		return "", nil
	}
	builder.WriteString(trailer)
	return builder.String(), memories
}

func escapeMemoryText(value string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	).Replace(value)
}

func appendInjectionEvent(store *ledger.Store, receipt InjectionReceipt, context Context) error {
	data, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode injection receipt: %w", err)
	}
	payload := ledger.InlinePayload("json", "application/json", string(data))
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion,
		EventID:       receipt.InjectionID,
		Kind:          ledger.KindInjection,
		ObservedAt:    receipt.RecordedAt,
		RecordedAt:    receipt.RecordedAt,
		Source: ledger.Source{
			Agent:          context.Agent,
			Adapter:        "agentmem-retrieval",
			AdapterVersion: AdapterVersion,
			DeviceID:       store.DeviceID(),
			ThreadID:       threadIDForContext(context),
			SessionID:      context.SessionID,
		},
		Payload:      &payload,
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Causality:    &ledger.Causality{ParentEventIDs: []string{receipt.RetrievalReceiptID}},
		Privacy:      ledger.Privacy{Classification: PrivacyLocalOnly},
	}
	if err := appendEvidenceWithRetry(store, event); err != nil {
		return fmt.Errorf("append injection receipt: %w", err)
	}
	return nil
}
