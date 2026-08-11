//go:build !windows

package evaluation

import "os"

func unsafeReviewPathInfo(info os.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}
