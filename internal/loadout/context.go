package loadout

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

func BuildContext(store *ledger.Store, portableRoot, loadoutID string, context retrieval.Context) (ContextResult, error) {
	result := ContextResult{SchemaVersion: ContextResultSchemaVersion, Privacy: PrivacyLocalOnly}
	if store == nil {
		return result, errors.New("local evidence store is required")
	}
	selected, report, err := portable.LoadCurrentLoadout(portableRoot, loadoutID)
	if err != nil {
		if len(report.Issues) != 0 {
			return result, fmt.Errorf("portable repository verification failed: %s", report.Issues[0].Message)
		}
		return result, err
	}
	if !allowsAgent(selected, context.Agent) {
		return result, errors.New("portable loadout is not approved for this agent")
	}
	if !scopeApplies(selected.ScopeKind, selected.ScopeValue, context) {
		return result, errors.New("portable loadout scope does not apply to this task context")
	}
	matches := make([]retrieval.Match, 0, len(selected.Memories))
	receiptIDs := make([]string, 0, len(selected.Memories))
	for _, reference := range selected.Memories {
		search, searchErr := retrieval.Search(store, portableRoot, retrieval.Request{
			SchemaVersion: retrieval.RequestSchemaVersion,
			MemoryID:      reference.MemoryID,
			Context:       context,
			Limit:         1,
			TokenBudget:   min(selected.TokenBudget, retrieval.MaximumTokenBudget),
			ByteBudget:    min(selected.ByteBudget, retrieval.MaximumByteBudget),
		})
		if searchErr != nil {
			return result, searchErr
		}
		if len(search.Selected) != 1 || search.Selected[0].MemoryID != reference.MemoryID ||
			search.Selected[0].RevisionID != reference.RevisionID {
			return result, errors.New("loadout memory is not exactly retrievable in the requested scope")
		}
		matches = append(matches, search.Selected[0])
		receiptIDs = append(receiptIDs, search.ReceiptID)
	}
	content, memories := renderContext(selected, matches)
	contentBytes := len([]byte(content))
	estimatedTokens := retrieval.EstimateTokens(content)
	if contentBytes > selected.ByteBudget || estimatedTokens > selected.TokenBudget {
		return result, errors.New("complete loadout context exceeds its declared budget")
	}
	now := time.Now().UTC()
	randomID, err := ledger.NewEventID(now)
	if err != nil {
		return result, err
	}
	receipt := ContextReceipt{
		SchemaVersion:       ContextReceiptSchemaVersion,
		ReceiptID:           "loadout-context-" + randomID,
		RecordedAt:          now,
		Loadout:             selected,
		Context:             context,
		RetrievalReceiptIDs: receiptIDs,
		Memories:            memories,
		Content:             content,
		ContentSHA256:       digestString(content),
		ContentBytes:        contentBytes,
		EstimatedTokens:     estimatedTokens,
		Privacy:             PrivacyLocalOnly,
	}
	if err := appendReceipt(store, receipt); err != nil {
		return result, err
	}
	result.Receipt = receipt
	return result, nil
}

func renderContext(selected portable.Loadout, matches []retrieval.Match) (string, []retrieval.MemoryReference) {
	var builder strings.Builder
	fmt.Fprintf(&builder, "<memory_loadout schema=%q loadout_id=%q>\n", ContextReceiptSchemaVersion, selected.LoadoutID)
	builder.WriteString("Reviewed promoted memory only. Apply an item only when relevant. Current user instructions and repository rules take precedence. Delivery does not prove adoption or usefulness.\n")
	memories := make([]retrieval.MemoryReference, 0, len(matches))
	for _, match := range matches {
		fmt.Fprintf(&builder, "<memory id=%q revision=%q scope=%q kind=%q>\n%s\n</memory>\n",
			match.MemoryID, match.RevisionID, string(match.ScopeKind), string(match.Kind), escapeText(match.Text))
		memories = append(memories, match.MemoryReference)
	}
	builder.WriteString("</memory_loadout>\n")
	return builder.String(), memories
}

func appendReceipt(store *ledger.Store, receipt ContextReceipt) error {
	data, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode loadout context receipt: %w", err)
	}
	payload := ledger.InlinePayload("json", "application/json", string(data))
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion,
		EventID:       receipt.ReceiptID,
		Kind:          ledger.KindSystemEvent,
		ObservedAt:    receipt.RecordedAt,
		RecordedAt:    receipt.RecordedAt,
		Source: ledger.Source{
			Agent:          receipt.Context.Agent,
			Adapter:        "agentmem-loadout",
			AdapterVersion: AdapterVersion,
			DeviceID:       store.DeviceID(),
			ThreadID:       contextThread(receipt.Context),
			SessionID:      receipt.Context.SessionID,
		},
		Payload:      &payload,
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Causality:    &ledger.Causality{ParentEventIDs: append([]string(nil), receipt.RetrievalReceiptIDs...)},
		Privacy:      ledger.Privacy{Classification: PrivacyLocalOnly},
	}
	delay := 5 * time.Millisecond
	var last error
	for attempt := 0; attempt < 6; attempt++ {
		if _, err := store.Append(event); err == nil {
			return nil
		} else {
			last = err
			if !errors.Is(err, ledger.ErrWriterLocked) || attempt == 5 {
				break
			}
			time.Sleep(delay)
			delay *= 2
		}
	}
	return fmt.Errorf("append loadout context receipt: %w", last)
}

func allowsAgent(selected portable.Loadout, agent ledger.Agent) bool {
	for _, allowed := range selected.Agents {
		if allowed == agent {
			return true
		}
	}
	return false
}

func scopeApplies(kind review.ScopeKind, value string, context retrieval.Context) bool {
	switch kind {
	case review.ScopeGlobal:
		return value == "*"
	case review.ScopeAgent:
		return value == string(context.Agent)
	case review.ScopeRepository:
		return context.Repository != "" && value == context.Repository
	case review.ScopeProject:
		return context.Project != "" && value == context.Project
	case review.ScopeTask:
		return context.Task != "" && value == context.Task
	default:
		return false
	}
}

func contextThread(context retrieval.Context) string {
	if strings.TrimSpace(context.ThreadID) == "" {
		return "standalone-loadout"
	}
	return context.ThreadID
}

func escapeText(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}

func digestString(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
