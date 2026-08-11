//go:build !windows && !darwin

package autosync

import (
	"context"
	"errors"
)

type unsupportedScheduler struct{}

func newPlatformScheduler() Scheduler {
	return unsupportedScheduler{}
}

func (unsupportedScheduler) Kind() string {
	return "unsupported"
}

func (unsupportedScheduler) Install(context.Context, Registration) error {
	return errors.New("automatic synchronization scheduling is supported on Windows and macOS")
}

func (unsupportedScheduler) Remove(context.Context, string) error {
	return nil
}

func (unsupportedScheduler) Exists(context.Context, string) (bool, error) {
	return false, nil
}
