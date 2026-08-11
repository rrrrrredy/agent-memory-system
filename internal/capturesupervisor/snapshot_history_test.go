package capturesupervisor

import (
	"context"
	"reflect"
	"testing"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestEvaluationSnapshotReplaysHistoricalSuccessfulRunAndRejectsTampering(t *testing.T) {
	fixture := newFixture(t)
	rollout := writeCodexRollout(t, fixture.codex, "first")
	configureCodex(t, fixture)

	firstRun, err := Run(context.Background(), fixture.store, RunOptions{Now: fixture.clock})
	if err != nil || !firstRun.FullReconcile || firstRun.Outcome != "success" {
		t.Fatalf("first full capture failed: result=%+v err=%v", firstRun, err)
	}
	first, err := LoadEvaluationSnapshot(fixture.store, []ledger.Agent{ledger.AgentCodex})
	if err != nil {
		t.Fatal(err)
	}

	writeRolloutContent(t, rollout, "second")
	secondRun, err := Run(context.Background(), fixture.store, RunOptions{Now: fixture.clock})
	if err != nil || secondRun.Outcome != "success" {
		t.Fatalf("second capture failed: result=%+v err=%v", secondRun, err)
	}

	replayed, err := LoadEvaluationSnapshotForAudit(
		fixture.store, first.AuditEventSHA256, []ledger.Agent{ledger.AgentCodex},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed, first) {
		t.Fatalf("historical snapshot changed after a later run:\nfirst=%+v\nreplayed=%+v", first, replayed)
	}
	if err := VerifyEvaluationSnapshot(fixture.store, first, []ledger.Agent{ledger.AgentCodex}); err != nil {
		t.Fatalf("historical snapshot verification failed: %v", err)
	}

	tampered := first
	tampered.RunID += "-tampered"
	tampered.SnapshotSHA256 = ""
	tampered.SnapshotSHA256, err = evaluationSnapshotSHA256(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyEvaluationSnapshot(fixture.store, tampered, []ledger.Agent{ledger.AgentCodex}); err == nil {
		t.Fatal("snapshot tampering was accepted after recomputing its self-hash")
	}
}
