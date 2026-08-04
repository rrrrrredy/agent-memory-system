//go:build darwin

package autosync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type launchdScheduler struct {
	run    commandRunner
	home   string
	domain string
}

func newPlatformScheduler() Scheduler {
	home, _ := os.UserHomeDir()
	return &launchdScheduler{
		run:    runSystemCommand,
		home:   home,
		domain: "gui/" + strconv.Itoa(os.Getuid()),
	}
}

func (scheduler *launchdScheduler) Kind() string {
	return "launchd"
}

func (scheduler *launchdScheduler) Install(ctx context.Context, registration Registration) error {
	data, err := launchAgentPlist(registration)
	if err != nil {
		return err
	}
	path, err := scheduler.path(registration.TaskID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create LaunchAgents directory: %w", err)
	}
	previous, previousErr := os.ReadFile(path)
	hadPrevious := previousErr == nil
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return fmt.Errorf("read existing launch agent: %w", previousErr)
	}
	wasLoaded, err := scheduler.Exists(ctx, registration.TaskID)
	if err != nil {
		return err
	}
	if wasLoaded {
		if err := scheduler.bootout(ctx, registration.TaskID); err != nil {
			return err
		}
	}
	if err := atomicWrite(path, data, 0o600); err != nil {
		if wasLoaded {
			_, _ = scheduler.run(ctx, "launchctl", "bootstrap", scheduler.domain, path)
		}
		return err
	}
	exitCode, runErr := scheduler.run(ctx, "launchctl", "bootstrap", scheduler.domain, path)
	if runErr == nil && exitCode == 0 {
		return nil
	}
	if hadPrevious {
		_ = atomicWrite(path, previous, 0o600)
	} else {
		_ = os.Remove(path)
	}
	if wasLoaded && hadPrevious {
		_, _ = scheduler.run(context.Background(), "launchctl", "bootstrap", scheduler.domain, path)
	}
	if runErr != nil {
		return runErr
	}
	return errors.New("launchd rejected automatic synchronization registration")
}

func (scheduler *launchdScheduler) Remove(ctx context.Context, id string) error {
	path, err := scheduler.path(id)
	if err != nil {
		return err
	}
	exists, err := scheduler.Exists(ctx, id)
	if err != nil {
		return err
	}
	if exists {
		if err := scheduler.bootout(ctx, id); err != nil {
			return err
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove launch agent: %w", err)
	}
	return nil
}

func (scheduler *launchdScheduler) Exists(ctx context.Context, id string) (bool, error) {
	exitCode, err := scheduler.run(ctx, "launchctl", "print", scheduler.domain+"/"+launchAgentLabel(id))
	if err != nil {
		return false, err
	}
	if exitCode == 0 {
		return true, nil
	}
	return false, nil
}

func (scheduler *launchdScheduler) bootout(ctx context.Context, id string) error {
	exitCode, err := scheduler.run(ctx, "launchctl", "bootout", scheduler.domain+"/"+launchAgentLabel(id))
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return errors.New("launchd could not remove automatic synchronization")
	}
	return nil
}

func (scheduler *launchdScheduler) path(id string) (string, error) {
	if scheduler.home == "" {
		return "", errors.New("user home directory is unavailable")
	}
	return filepath.Join(scheduler.home, "Library", "LaunchAgents", launchAgentLabel(id)+".plist"), nil
}
