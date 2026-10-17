// Package mediafs performs every file-system operation on the media tree.
//
// All access goes through an os.Root opened at MEDIA_ROOT, so path traversal
// and symlinks pointing outside MEDIA_ROOT are rejected by the runtime
// (openat2 RESOLVE_BENEATH on Linux, component-wise checks elsewhere).
// Paths passed to this package are slash-separated and relative to MEDIA_ROOT.
package mediafs

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"syscall"
	"unicode/utf8"
)

var (
	// ErrConflict means the destination already exists.
	ErrConflict = errors.New("destination already exists")
	// ErrCrossDevice means staging and library are on different file systems,
	// so an atomic rename is impossible.
	ErrCrossDevice = errors.New("staging directory and library are on different file systems; mount them as one volume")
	// ErrBadPath means a path failed validation.
	ErrBadPath = errors.New("invalid path")
)

// Perms describes ownership and modes applied to everything published.
type Perms struct {
	UID, GID int // -1 = leave unchanged
	FileMode os.FileMode
	DirMode  os.FileMode
}

// FS is the media tree rooted at MEDIA_ROOT.
type FS struct {
	root     *os.Root
	rootPath string
	perms    Perms
	log      *slog.Logger
	noLinks  atomic.Bool // set once hard links turned out unsupported
}

// Open opens the media root. It must already exist.
func Open(rootPath string, perms Perms, log *slog.Logger) (*FS, error) {
	r, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open MEDIA_ROOT: %w", err)
	}
	return &FS{root: r, rootPath: rootPath, perms: perms, log: log}, nil
}

// Close releases the root.
func (f *FS) Close() error { return f.root.Close() }

// Healthy checks that the media root is still accessible.
func (f *FS) Healthy() error {
	_, err := f.root.Stat(".")
	return err
}

// RootPath returns the absolute media root.
func (f *FS) RootPath() string { return f.rootPath }

// CheckRel validates a relative path built by the server. It is a second line
// of defense; os.Root enforces confinement regardless.
func CheckRel(rel string) error {
	if rel == "" || !utf8.ValidString(rel) || strings.ContainsAny(rel, "\x00\\") ||
		strings.HasPrefix(rel, "/") || path.Clean(rel) != rel {
		return fmt.Errorf("%w: %q", ErrBadPath, rel)
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("%w: %q", ErrBadPath, rel)
		}
	}
	return nil
}

// Exists reports whether rel exists (without following a final symlink).
func (f *FS) Exists(rel string) (bool, error) {
	if err := CheckRel(rel); err != nil {
		return false, err
	}
	_, err := f.root.Lstat(rel)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// ReadDir lists a directory.
func (f *FS) ReadDir(rel string) ([]fs.DirEntry, error) {
	if err := CheckRel(rel); err != nil {
		return nil, err
	}
	d, err := f.root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return d.ReadDir(-1)
}

// ReadRoot lists MEDIA_ROOT itself.
func (f *FS) ReadRoot() ([]fs.DirEntry, error) {
	d, err := f.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return d.ReadDir(-1)
}

// Lstat stats rel without following a final symlink.
func (f *FS) Lstat(rel string) (fs.FileInfo, error) {
	if err := CheckRel(rel); err != nil {
		return nil, err
	}
	return f.root.Lstat(rel)
}

// EnsureDir creates rel and missing parents. Newly created directories get
// DirMode and the configured owner. It returns permission warnings (chown
// failures) separately from hard errors.
func (f *FS) EnsureDir(rel string, applyPerms bool) (warn error, err error) {
	if err := CheckRel(rel); err != nil {
		return nil, err
	}
	segs := strings.Split(rel, "/")
	for i := range segs {
		cur := strings.Join(segs[:i+1], "/")
		fi, err := f.root.Lstat(cur)
		if err == nil {
			if !fi.IsDir() {
				return warn, fmt.Errorf("%s exists and is not a directory", cur)
			}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return warn, err
		}
		if err := f.root.Mkdir(cur, f.perms.DirMode); err != nil && !errors.Is(err, fs.ErrExist) {
			return warn, err
		}
		if applyPerms {
			w, err := f.applyPerms(cur, f.perms.DirMode)
			if err != nil {
				return warn, err
			}
			if warn == nil {
				warn = w
			}
		}
	}
	return warn, nil
}

// applyPerms sets mode and owner. chmod failures are errors; chown failures
// are returned as a warning because a non-root process can only chown to
// itself and its own groups.
func (f *FS) applyPerms(rel string, mode os.FileMode) (warn, err error) {
	if err := f.root.Chmod(rel, mode); err != nil {
		return nil, fmt.Errorf("chmod %s: %w", rel, err)
	}
	if f.perms.UID < 0 && f.perms.GID < 0 {
		return nil, nil
	}
	if err := f.root.Lchown(rel, f.perms.UID, f.perms.GID); err != nil {
		return fmt.Errorf("chown %s to %d:%d: %w", rel, f.perms.UID, f.perms.GID, err), nil
	}
	return nil, nil
}

// CreateExclusive creates a new file for writing; it fails if rel exists.
func (f *FS) CreateExclusive(rel string, mode os.FileMode) (*os.File, error) {
	if err := CheckRel(rel); err != nil {
		return nil, err
	}
	return f.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
}

// OpenWrite opens an existing file for writing.
func (f *FS) OpenWrite(rel string) (*os.File, error) {
	if err := CheckRel(rel); err != nil {
		return nil, err
	}
	return f.root.OpenFile(rel, os.O_WRONLY, 0)
}

// Remove removes a file. Only used for staging data, never for library files.
func (f *FS) Remove(rel string) error {
	if err := CheckRel(rel); err != nil {
		return err
	}
	return f.root.Remove(rel)
}

// Policy decides what happens when the destination exists.
type Policy string

const (
	PolicyNone     Policy = ""          // fail with ErrConflict
	PolicyReplace  Policy = "replace"   // move the existing file to trash, then replace
	PolicyKeepBoth Policy = "keep_both" // publish under an alternative name
)

// PublishResult describes a completed publish.
type PublishResult struct {
	Path        string // final relative path
	TrashedPath string // where a replaced file went, if any
	PermWarning string // non-fatal ownership problem
}

// Publish moves a completed staged file to final without ever overwriting
// silently. Permissions are applied to the staged file first, so the
// destination appears complete, correctly named and with final permissions
// in a single atomic step.
//
// alt returns the n-th alternative destination (n >= 2) for PolicyKeepBoth.
// trash is the destination for a replaced file under PolicyReplace.
func (f *FS) Publish(staged, final string, policy Policy, alt func(n int) string, trash string) (PublishResult, error) {
	res := PublishResult{Path: final}
	for _, p := range []string{staged, final} {
		if err := CheckRel(p); err != nil {
			return res, err
		}
	}
	if fi, err := f.root.Lstat(staged); err != nil {
		return res, fmt.Errorf("staged file: %w", err)
	} else if !fi.Mode().IsRegular() {
		return res, fmt.Errorf("%w: staged file is not a regular file", ErrBadPath)
	}

	warn, err := f.EnsureDir(path.Dir(final), true)
	if err != nil {
		return res, err
	}
	w, err := f.applyPerms(staged, f.perms.FileMode)
	if err != nil {
		return res, err
	}
	if warn == nil {
		warn = w
	}
	if warn != nil {
		res.PermWarning = warn.Error()
	}

	err = f.linkNoReplace(staged, final)
	if !errors.Is(err, ErrConflict) {
		if err == nil {
			f.syncDir(path.Dir(final))
		}
		return res, err
	}

	switch policy {
	case PolicyKeepBoth:
		if alt == nil {
			return res, ErrConflict
		}
		for n := 2; n < 100; n++ {
			cand := alt(n)
			if err := CheckRel(cand); err != nil {
				return res, err
			}
			err := f.linkNoReplace(staged, cand)
			if errors.Is(err, ErrConflict) {
				continue
			}
			if err == nil {
				res.Path = cand
				f.syncDir(path.Dir(cand))
			}
			return res, err
		}
		return res, fmt.Errorf("%w: no free alternative name", ErrConflict)

	case PolicyReplace:
		if err := CheckRel(trash); err != nil {
			return res, err
		}
		if _, err := f.EnsureDir(path.Dir(trash), false); err != nil {
			return res, fmt.Errorf("trash: %w", err)
		}
		if fi, err := f.root.Lstat(final); err != nil {
			return res, err
		} else if !fi.Mode().IsRegular() {
			return res, fmt.Errorf("%w: refusing to replace a non-regular file", ErrConflict)
		}
		// Preferred: keep the old file reachable via a hard link in the trash,
		// then atomically swap the new file in. The destination never vanishes.
		if err := f.root.Link(final, trash); err == nil {
			if err := f.root.Rename(staged, final); err != nil {
				_ = f.root.Remove(trash) // old file is still at final
				return res, err
			}
		} else if linkUnsupported(err) {
			if err := f.root.Rename(final, trash); err != nil {
				return res, err
			}
			if err := f.linkNoReplace(staged, final); err != nil {
				return res, err
			}
		} else {
			return res, fmt.Errorf("move replaced file to trash: %w", err)
		}
		res.TrashedPath = trash
		f.syncDir(path.Dir(final))
		return res, nil
	}
	return res, ErrConflict
}

// MoveToTrash moves a regular library file to trash (never deletes). Used
// when the user chose "replace" and the episode exists under another name.
func (f *FS) MoveToTrash(rel, trash string) error {
	for _, p := range []string{rel, trash} {
		if err := CheckRel(p); err != nil {
			return err
		}
	}
	fi, err := f.root.Lstat(rel)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: not a regular file", ErrBadPath)
	}
	if _, err := f.EnsureDir(path.Dir(trash), false); err != nil {
		return err
	}
	if err := f.linkNoReplace(rel, trash); err != nil {
		return fmt.Errorf("move %s to trash: %w", rel, err)
	}
	return nil
}

// Restore moves a trashed file back to original, only if that path is free.
func (f *FS) Restore(trash, original string) error {
	for _, p := range []string{trash, original} {
		if err := CheckRel(p); err != nil {
			return err
		}
	}
	fi, err := f.root.Lstat(trash)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: not a regular file", ErrBadPath)
	}
	if _, err := f.EnsureDir(path.Dir(original), true); err != nil {
		return err
	}
	return f.linkNoReplace(trash, original)
}

// PruneEmptyDirs removes empty directories from dir upwards, never removing
// stop itself or anything outside it. Non-empty directories end the walk.
func (f *FS) PruneEmptyDirs(dir, stop string) {
	for dir != stop && strings.HasPrefix(dir, stop+"/") && CheckRel(dir) == nil {
		fi, err := f.root.Lstat(dir)
		if err != nil || !fi.IsDir() {
			return
		}
		if err := f.root.Remove(dir); err != nil { // fails unless empty
			return
		}
		dir = path.Dir(dir)
	}
}

// RemoveAll deletes a tree. Only used to purge expired trash entries.
func (f *FS) RemoveAll(rel string) error {
	if err := CheckRel(rel); err != nil {
		return err
	}
	return f.root.RemoveAll(rel)
}

// WalkFiles lists regular files below dir up to maxDepth levels, skipping
// hidden entries and symlinks. Paths are relative to MEDIA_ROOT.
func (f *FS) WalkFiles(dir string, maxDepth int) ([]string, error) {
	var out []string
	var walk func(d string, depth int) error
	walk = func(d string, depth int) error {
		entries, err := f.ReadDir(d)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") || e.Type()&fs.ModeSymlink != 0 {
				continue
			}
			p := d + "/" + e.Name()
			switch {
			case e.IsDir() && depth < maxDepth:
				if err := walk(p, depth+1); err != nil {
					return err
				}
			case e.Type().IsRegular():
				out = append(out, p)
			}
		}
		return nil
	}
	return out, walk(dir, 1)
}

// linkNoReplace atomically makes old available as new, failing with
// ErrConflict if new exists. A hard link fails atomically with EEXIST, so
// there is no window in which an existing file can be overwritten.
func (f *FS) linkNoReplace(old, new string) error {
	if !f.noLinks.Load() {
		err := f.root.Link(old, new)
		switch {
		case err == nil:
			if err := f.root.Remove(old); err != nil {
				f.log.Warn("could not remove staged file after publish", "path", old, "err", err)
			}
			return nil
		case errors.Is(err, fs.ErrExist):
			return ErrConflict
		case errors.Is(err, syscall.EXDEV):
			return ErrCrossDevice
		case linkUnsupported(err):
			f.noLinks.Store(true)
			f.log.Warn("file system does not support hard links; falling back to check-then-rename (a concurrent writer could race)", "err", err)
		default:
			return err
		}
	}
	if _, err := f.root.Lstat(new); err == nil {
		return ErrConflict
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	err := f.root.Rename(old, new)
	if errors.Is(err, syscall.EXDEV) {
		return ErrCrossDevice
	}
	return err
}

func linkUnsupported(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOSYS) ||
		errors.Is(err, syscall.EMLINK)
}

// syncDir fsyncs a directory so a published name survives a crash. Best effort.
func (f *FS) syncDir(rel string) {
	d, err := f.root.Open(rel)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
