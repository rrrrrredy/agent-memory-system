//go:build windows

package autosync

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestWindowsSchedulerLifecycleUsesCurrentUserLimitedTask(t *testing.T) {
	type call struct {
		name string
		args []string
	}
	calls := []call{}
	registered := false
	runner := func(_ context.Context, name string, args ...string) (int, error) {
		calls = append(calls, call{name: name, args: append([]string(nil), args...)})
		switch args[0] {
		case "/Create":
			registered = true
			return 0, nil
		case "/Query":
			if registered {
				return 0, nil
			}
			return 1, nil
		case "/Delete":
			registered = false
			return 0, nil
		default:
			t.Fatalf("unexpected schtasks arguments: %v", args)
			return -1, nil
		}
	}
	scheduler := &windowsScheduler{run: runner}
	registration := Registration{
		TaskID: "agent-memory-sync-0123456789abcdef", RepositoryRoot: `C:\Memory Data`,
		ExecutablePath: `C:\Program Files\Agent Memory\agentmem.exe`, Interval: 15 * time.Minute,
	}
	if err := scheduler.Install(context.Background(), registration); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].name != "schtasks.exe" {
		t.Fatalf("unexpected scheduler calls: %+v", calls)
	}
	joined := strings.Join(calls[0].args, " ")
	for _, expected := range []string{"/SC MINUTE", "/MO 15", "/RL LIMITED", "--scheduled"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("registration is missing %q: %s", expected, joined)
		}
	}
	if strings.Contains(joined, "origin") || strings.Contains(joined, "evidence") {
		t.Fatalf("registration contains data not required by the scheduler: %s", joined)
	}
	if err := scheduler.Remove(context.Background(), registration.TaskID); err != nil {
		t.Fatal(err)
	}
	if registered {
		t.Fatal("scheduled task remained registered")
	}
}
