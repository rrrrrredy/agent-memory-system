//go:build darwin

package autosync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLaunchdSchedulerLifecycleWritesAndRemovesUserAgent(t *testing.T) {
	loaded := false
	runner := func(_ context.Context, name string, args ...string) (int, error) {
		if name != "launchctl" {
			t.Fatalf("unexpected command: %s", name)
		}
		switch args[0] {
		case "print":
			if loaded {
				return 0, nil
			}
			return 113, nil
		case "bootstrap":
			loaded = true
			return 0, nil
		case "bootout":
			loaded = false
			return 0, nil
		default:
			t.Fatalf("unexpected launchctl arguments: %v", args)
			return -1, nil
		}
	}
	home := t.TempDir()
	scheduler := &launchdScheduler{run: runner, home: home, domain: "gui/501"}
	registration := Registration{
		TaskID: "agent-memory-sync-0123456789abcdef", RepositoryRoot: "/Users/song/Memory Data",
		ExecutablePath: "/Users/song/bin/agentmem", Interval: 15 * time.Minute,
	}
	if err := scheduler.Install(context.Background(), registration); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel(registration.TaskID)+".plist")
	if data, err := os.ReadFile(path); err != nil || len(data) == 0 || !loaded {
		t.Fatalf("launch agent was not installed: loaded=%v bytes=%d err=%v", loaded, len(data), err)
	}
	if err := scheduler.Remove(context.Background(), registration.TaskID); err != nil {
		t.Fatal(err)
	}
	if loaded {
		t.Fatal("launch agent remained loaded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("launch agent file remained: %v", err)
	}
}
