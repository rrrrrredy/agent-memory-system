package retrieval

import (
	"errors"
	"fmt"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

// ResolveVerifiedInjection returns the exact delivered memory context only
// after the complete retrieval receipt graph has passed replay verification.
func ResolveVerifiedInjection(store *ledger.Store, injectionID string) (InjectionReceipt, error) {
	if store == nil || strings.TrimSpace(injectionID) == "" {
		return InjectionReceipt{}, errors.New("store and injection id are required")
	}
	report := Verify(store)
	if len(report.Issues) != 0 {
		return InjectionReceipt{}, fmt.Errorf("retrieval receipt verification failed: %s", strings.Join(report.Issues, "; "))
	}
	var result InjectionReceipt
	found := false
	err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.EventID != injectionID {
			return nil
		}
		if found {
			return errors.New("injection id appears more than once")
		}
		if record.Event.Kind != ledger.KindInjection {
			return errors.New("requested event is not an injection receipt")
		}
		if err := decodeReceiptEvent(record.Event, &result); err != nil {
			return err
		}
		if issues := validateInjectionReceipt(record.Event, result); len(issues) != 0 {
			return errors.New(strings.Join(issues, "; "))
		}
		found = true
		return nil
	})
	if err != nil {
		return InjectionReceipt{}, fmt.Errorf("resolve injection receipt: %w", err)
	}
	if !found {
		return InjectionReceipt{}, errors.New("verified injection receipt is unavailable")
	}
	if result.Content == "" || len(result.Memories) == 0 {
		return InjectionReceipt{}, errors.New("verified injection contains no delivered memory")
	}
	return result, nil
}

// ListVerifiedRetrievals returns a snapshot-scoped receipt index after the
// complete retrieval graph has passed replay verification once.
func ListVerifiedRetrievals(store *ledger.Store) (map[string]Receipt, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	report := Verify(store)
	if len(report.Issues) != 0 {
		return nil, fmt.Errorf("retrieval receipt verification failed: %s", strings.Join(report.Issues, "; "))
	}
	result := map[string]Receipt{}
	err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.Kind != ledger.KindRetrieval {
			return nil
		}
		var receipt Receipt
		if err := decodeReceiptEvent(record.Event, &receipt); err != nil {
			return err
		}
		if _, duplicate := result[receipt.ReceiptID]; duplicate {
			return errors.New("retrieval receipt id appears more than once")
		}
		result[receipt.ReceiptID] = receipt
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("index verified retrieval receipts: %w", err)
	}
	return result, nil
}

// ResolveVerifiedRetrieval returns the exact retrieval receipt only after the
// complete retrieval, injection, and adoption graph has passed replay
// verification.
func ResolveVerifiedRetrieval(store *ledger.Store, receiptID string) (Receipt, error) {
	if store == nil || strings.TrimSpace(receiptID) == "" {
		return Receipt{}, errors.New("store and retrieval receipt id are required")
	}
	report := Verify(store)
	if len(report.Issues) != 0 {
		return Receipt{}, fmt.Errorf("retrieval receipt verification failed: %s", strings.Join(report.Issues, "; "))
	}
	var result Receipt
	found := false
	err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.EventID != receiptID {
			return nil
		}
		if found {
			return errors.New("retrieval receipt id appears more than once")
		}
		if record.Event.Kind != ledger.KindRetrieval {
			return errors.New("requested event is not a retrieval receipt")
		}
		if err := decodeReceiptEvent(record.Event, &result); err != nil {
			return err
		}
		if issues := validateReceipt(record.Event, result); len(issues) != 0 {
			return errors.New(strings.Join(issues, "; "))
		}
		found = true
		return nil
	})
	if err != nil {
		return Receipt{}, fmt.Errorf("resolve retrieval receipt: %w", err)
	}
	if !found {
		return Receipt{}, errors.New("verified retrieval receipt is unavailable")
	}
	return result, nil
}
