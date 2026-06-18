//go:build unix

package e2e

import (
	"os"
	"syscall"
)

// inodeOf returns the inode number for a file on unix systems. The boolean is
// false if the underlying stat data is unavailable.
func inodeOf(fi os.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Ino, true
}
