//go:build !windows

package autosync

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
