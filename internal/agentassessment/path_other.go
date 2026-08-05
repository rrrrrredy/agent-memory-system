//go:build !windows

package agentassessment

import "os"

func unsafePathInfo(info os.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}
