//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"syscall"
)

// freeBytes returns the space available at path; ok is false if unknown.
func freeBytes(path string) (free uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true
}

// sameDevice reports whether both paths are on one volume. A path that does not
// exist yet is checked through its parent.
func sameDevice(a, b string) bool {
	da, okA := deviceOf(a)
	db, okB := deviceOf(b)
	return okA && okB && da == db
}

func deviceOf(path string) (uint64, bool) {
	for {
		info, err := os.Stat(path)
		if err == nil {
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return 0, false
			}
			return uint64(st.Dev), true
		}
		parent := filepath.Dir(path)
		if parent == path {
			return 0, false
		}
		path = parent
	}
}
