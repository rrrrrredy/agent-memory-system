package loadout

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

func Verify(store *ledger.Store) VerificationReport {
	_, report := buildVerifiedContexts(store)
	return report
}

// ListVerifiedContexts returns a snapshot-scoped context index after the
// complete retrieval and loadout graphs have each been verified once.
func ListVerifiedContexts(store *ledger.Store) (map[string]ContextReceipt, error) {
	contexts, report := buildVerifiedContexts(store)
	if len(report.Issues) != 0 {
		return nil, fmt.Errorf("loadout context verification failed: %s", strings.Join(report.Issues, "; "))
	}
	return contexts, nil
}

func buildVerifiedContexts(store *ledger.Store) (map[string]ContextReceipt, VerificationReport) {
	report := VerificationReport{
		SchemaVersion: VerificationSchemaVersion,
		Issues:        []string{},
		Privacy:       PrivacyLocalOnly,
	}
	contexts := map[string]ContextReceipt{}
	if store == nil {
		report.Issues = append(report.Issues, "local evidence store is required")
		return contexts, report
	}
	if ledgerReport := store.Verify(); len(ledgerReport.Issues) != 0 {
		report.Issues = append(report.Issues, "evidence ledger verification failed")
		return contexts, report
	}
	retrievals, retrievalErr := retrieval.ListVerifiedRetrievals(store)
	if retrievalErr != nil {
		report.Issues = append(report.Issues, "retrieval receipt verification failed")
		return contexts, report
	}
	err := store.VisitRecords(func(record ledger.Record) error {
		receipt, applies, err := decodeEvent(record.Event)
		if err != nil {
			report.Issues = append(report.Issues, err.Error())
			return nil
		}
		if !applies {
			return nil
		}
		if issues := validateReceipt(record.Event, receipt, retrievals); len(issues) != 0 {
			report.Issues = append(report.Issues, issues...)
			return nil
		}
		report.ReceiptsChecked++
		contexts[receipt.ReceiptID] = receipt
		return nil
	})
	if err != nil {
		report.Issues = append(report.Issues, "loadout context ledger traversal failed")
	}
	sort.Strings(report.Issues)
	return contexts, report
}

func ResolveVerifiedContext(store *ledger.Store, receiptID string) (ContextReceipt, error) {
	if strings.TrimSpace(receiptID) == "" {
		return ContextReceipt{}, errors.New("loadout context receipt id is required")
	}
	report := Verify(store)
	if len(report.Issues) != 0 {
		return ContextReceipt{}, fmt.Errorf("loadout context verification failed: %s", strings.Join(report.Issues, "; "))
	}
	found := false
	var result ContextReceipt
	err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.EventID != receiptID {
			return nil
		}
		receipt, applies, err := decodeEvent(record.Event)
		if err != nil {
			return err
		}
		if !applies || found {
			return errors.New("loadout context receipt identity is invalid or duplicated")
		}
		found = true
		result = receipt
		return nil
	})
	if err != nil {
		return ContextReceipt{}, err
	}
	if !found {
		return ContextReceipt{}, errors.New("verified loadout context receipt is unavailable")
	}
	return result, nil
}

func decodeEvent(event ledger.Event) (ContextReceipt, bool, error) {
	if event.Kind != ledger.KindSystemEvent || event.Payload == nil || event.Payload.Content == nil ||
		event.Payload.Encoding != "json" || event.Payload.MediaType != "application/json" {
		return ContextReceipt{}, false, nil
	}
	var header struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal([]byte(*event.Payload.Content), &header); err != nil ||
		header.SchemaVersion != ContextReceiptSchemaVersion {
		return ContextReceipt{}, false, nil
	}
	decoder := json.NewDecoder(bytes.NewBufferString(*event.Payload.Content))
	decoder.DisallowUnknownFields()
	var receipt ContextReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return ContextReceipt{}, true, errors.New("loadout context receipt is malformed")
	}
	if err := requireEOF(decoder); err != nil {
		return ContextReceipt{}, true, errors.New("loadout context receipt has trailing data")
	}
	return receipt, true, nil
}

func validateReceipt(event ledger.Event, receipt ContextReceipt,
	retrievals map[string]retrieval.Receipt) []string {
	issues := []string{}
	add := func(ok bool, message string) {
		if !ok {
			issues = append(issues, receipt.ReceiptID+": "+message)
		}
	}
	add(receipt.SchemaVersion == ContextReceiptSchemaVersion && receipt.Privacy == PrivacyLocalOnly,
		"receipt envelope is invalid")
	add(receipt.ReceiptID == event.EventID && receipt.RecordedAt.Equal(event.RecordedAt) &&
		event.ObservedAt.Equal(event.RecordedAt), "event identity or time does not match")
	add(event.Source.Adapter == "agentmem-loadout" && event.Source.AdapterVersion == AdapterVersion &&
		event.Source.Agent == receipt.Context.Agent && event.Source.ThreadID == contextThread(receipt.Context) &&
		event.Source.SessionID == receipt.Context.SessionID, "event source does not match")
	add(event.Privacy.Classification == PrivacyLocalOnly &&
		event.Completeness.Status == ledger.CompletenessComplete, "event trust boundary is invalid")
	add(event.Causality != nil && reflect.DeepEqual(event.Causality.ParentEventIDs, receipt.RetrievalReceiptIDs),
		"event causality does not match retrieval receipts")
	add(portable.ValidateLoadout(receipt.Loadout) == nil, "embedded loadout is invalid")
	add(allowsAgent(receipt.Loadout, receipt.Context.Agent) &&
		scopeApplies(receipt.Loadout.ScopeKind, receipt.Loadout.ScopeValue, receipt.Context),
		"embedded loadout is not applicable to the context")
	add(len(receipt.RetrievalReceiptIDs) == len(receipt.Loadout.Memories) &&
		len(receipt.Memories) == len(receipt.Loadout.Memories), "memory and retrieval counts do not match")
	matches := make([]retrieval.Match, 0, len(receipt.Loadout.Memories))
	if len(issues) == 0 {
		for index, reference := range receipt.Loadout.Memories {
			resolved, verified := retrievals[receipt.RetrievalReceiptIDs[index]]
			if !verified || resolved.Request.MemoryID != reference.MemoryID ||
				!reflect.DeepEqual(resolved.Request.Context, receipt.Context) || len(resolved.Result.Selected) != 1 ||
				resolved.Result.Selected[0].MemoryID != reference.MemoryID ||
				resolved.Result.Selected[0].RevisionID != reference.RevisionID ||
				receipt.Memories[index].MemoryID != reference.MemoryID ||
				receipt.Memories[index].RevisionID != reference.RevisionID {
				issues = append(issues, receipt.ReceiptID+": retrieval does not match the ordered loadout")
				break
			}
			matches = append(matches, resolved.Result.Selected[0])
		}
	}
	if len(matches) == len(receipt.Loadout.Memories) {
		content, memories := renderContext(receipt.Loadout, matches)
		add(content == receipt.Content && reflect.DeepEqual(memories, receipt.Memories),
			"rendered context does not match its exact sources")
	}
	add(receipt.Content != "" && digestString(receipt.Content) == receipt.ContentSHA256 &&
		len([]byte(receipt.Content)) == receipt.ContentBytes &&
		retrieval.EstimateTokens(receipt.Content) == receipt.EstimatedTokens,
		"content measurements do not match")
	add(receipt.ContentBytes <= receipt.Loadout.ByteBudget &&
		receipt.EstimatedTokens <= receipt.Loadout.TokenBudget, "content exceeds loadout budget")
	return issues
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}
