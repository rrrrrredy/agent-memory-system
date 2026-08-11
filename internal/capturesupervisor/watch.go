package capturesupervisor

import (
	"context"
	"errors"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

// Watch runs capture immediately and then at the configured interval. It
// continues after audited source failures, but stops on configuration or audit
// failures that prevent a terminal run record.
func Watch(
	ctx context.Context, store *ledger.Store, options RunOptions,
	emit func(RunResult) error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if emit == nil {
		return errors.New("capture watch result handler is required")
	}
	for {
		result, runErr := Run(ctx, store, options)
		if result.RunID != "" && !result.FinishedAt.IsZero() && result.AuditSequence > 0 {
			if err := emit(result); err != nil {
				return err
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		if runErr != nil && result.AuditSequence == 0 {
			return runErr
		}
		state, err := replayAudit(store)
		if err != nil {
			return err
		}
		config, err := loadActiveConfig(store, state)
		if err != nil {
			return err
		}
		timer := time.NewTimer(time.Duration(config.IntervalSeconds) * time.Second)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}
