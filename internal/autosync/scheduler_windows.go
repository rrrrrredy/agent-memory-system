//go:build windows

package autosync

import (
	"context"
	"errors"
	"strconv"
	"time"
)

type windowsScheduler struct {
	run commandRunner
}

func newPlatformScheduler() Scheduler {
	return &windowsScheduler{run: runSystemCommand}
}

func (scheduler *windowsScheduler) Kind() string {
	return "windows_task_scheduler"
}

func (scheduler *windowsScheduler) Install(ctx context.Context, registration Registration) error {
	if err := validateRegistration(registration); err != nil {
		return err
	}
	exitCode, err := scheduler.run(ctx, "schtasks.exe",
		"/Create",
		"/SC", "MINUTE",
		"/MO", strconv.FormatInt(int64(registration.Interval/time.Minute), 10),
		"/TN", windowsTaskName(registration.TaskID),
		"/TR", windowsTaskCommand(registration),
		"/RL", "LIMITED",
		"/F",
	)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return errors.New("Windows Task Scheduler rejected automatic synchronization registration")
	}
	return nil
}

func (scheduler *windowsScheduler) Remove(ctx context.Context, id string) error {
	exists, err := scheduler.Exists(ctx, id)
	if err != nil || !exists {
		return err
	}
	exitCode, err := scheduler.run(ctx, "schtasks.exe", "/Delete", "/TN", windowsTaskName(id), "/F")
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return errors.New("Windows Task Scheduler could not remove automatic synchronization")
	}
	return nil
}

func (scheduler *windowsScheduler) Exists(ctx context.Context, id string) (bool, error) {
	exitCode, err := scheduler.run(ctx, "schtasks.exe", "/Query", "/TN", windowsTaskName(id))
	if err != nil {
		return false, err
	}
	switch exitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, errors.New("Windows Task Scheduler registration could not be inspected")
	}
}
