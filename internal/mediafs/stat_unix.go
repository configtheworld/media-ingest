//go:build linux || darwin

package mediafs

import (
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// FreeBytes returns the bytes available to unprivileged users on the file
// system holding rel, or -1 if unknown.
func (f *FS) FreeBytes(rel string) int64 {
	if CheckRel(rel) != nil {
		return -1
	}
	var st unix.Statfs_t
	if err := unix.Statfs(filepath.Join(f.rootPath, filepath.FromSlash(rel)), &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// SameDevice reports whether two directories are on the same file system.
func (f *FS) SameDevice(a, b string) (bool, error) {
	da, err := f.dev(a)
	if err != nil {
		return false, err
	}
	db, err := f.dev(b)
	if err != nil {
		return false, err
	}
	return da == db, nil
}

func (f *FS) dev(rel string) (uint64, error) {
	fi, err := f.root.Stat(rel)
	if err != nil {
		return 0, err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), nil //nolint:unconvert // int32 on darwin
	}
	return 0, os.ErrInvalid
}
