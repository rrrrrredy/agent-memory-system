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
