package mediafs

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

func newFS(t *testing.T) (*FS, string) {
	t.Helper()
	dir := t.TempDir()
	f, err := Open(dir, Perms{UID: -1, GID: -1, FileMode: 0o644, DirMode: 0o755}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	for _, d := range []string{".incoming", "movies"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f, dir
}

func stage(t *testing.T, dir, name, content string) string {
	t.Helper()
	rel := ".incoming/" + name
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return rel
}

func TestCheckRel(t *testing.T) {
	bad := []string{"", "/etc/passwd", "../x", "a/../../x", "a/./b", "a//b", "a/", "a\x00b", `a\b`, "..", ".", "a/..", "\xff"}
	for _, p := range bad {
		if CheckRel(p) == nil {
			t.Errorf("CheckRel(%q) should fail", p)
		}
	}
	for _, p := range []string{"movies/A (2000)/A.mkv", ".incoming/x.part", "tv/Şahsiyet/Season 01"} {
		if err := CheckRel(p); err != nil {
			t.Errorf("CheckRel(%q): %v", p, err)
		}
	}
}

func TestSymlinkEscapeRejected(t *testing.T) {
	f, dir := newFS(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "movies", "evil")); err != nil {
		t.Fatal(err)
	}
	staged := stage(t, dir, "a.part", "data")
	_, err := f.Publish(staged, "movies/evil/A.mkv", PolicyNone, nil, "")
	if err == nil {
		t.Fatal("publish through a symlink escaping MEDIA_ROOT must fail")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("file escaped MEDIA_ROOT: %v", entries)
	}
	if _, err := f.EnsureDir("movies/evil/sub", true); err == nil {
		t.Fatal("EnsureDir through escaping symlink must fail")
	}
}

func TestPublishAtomicNoReplace(t *testing.T) {
	f, dir := newFS(t)
	staged := stage(t, dir, "1.part", "new")
	res, err := f.Publish(staged, "movies/A (2000)/A (2000).mkv", PolicyNone, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, res.Path))
	if string(got) != "new" {
		t.Fatalf("content %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, staged)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("staged file should be gone")
	}
	fi, _ := os.Stat(filepath.Join(dir, res.Path))
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("file mode %v", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Join(dir, "movies/A (2000)"))
	if di.Mode().Perm() != 0o755 {
		t.Errorf("dir mode %v", di.Mode().Perm())
	}

	// A second publish to the same path must not overwrite.
	staged2 := stage(t, dir, "2.part", "other")
	if _, err := f.Publish(staged2, res.Path, PolicyNone, nil, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	got, _ = os.ReadFile(filepath.Join(dir, res.Path))
	if string(got) != "new" {
		t.Fatal("existing file was overwritten")
	}
	if _, err := os.Stat(filepath.Join(dir, staged2)); err != nil {
		t.Fatal("staged file must be kept on conflict")
	}
}

func TestPublishKeepBoth(t *testing.T) {
	f, dir := newFS(t)
	os.MkdirAll(filepath.Join(dir, "movies/A"), 0o755)
	os.WriteFile(filepath.Join(dir, "movies/A/A.mkv"), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(dir, "movies/A/A - Version 2.mkv"), []byte("old2"), 0o644)
	staged := stage(t, dir, "1.part", "new")
	alt := func(n int) string { return fmt.Sprintf("movies/A/A - Version %d.mkv", n) }
	res, err := f.Publish(staged, "movies/A/A.mkv", PolicyKeepBoth, alt, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != "movies/A/A - Version 3.mkv" {
		t.Fatalf("path %q", res.Path)
	}
	for p, want := range map[string]string{"movies/A/A.mkv": "old", "movies/A/A - Version 2.mkv": "old2", res.Path: "new"} {
		if got, _ := os.ReadFile(filepath.Join(dir, p)); string(got) != want {
			t.Errorf("%s = %q want %q", p, got, want)
		}
	}
}

func TestPublishReplaceMovesOldToTrash(t *testing.T) {
	f, dir := newFS(t)
	os.MkdirAll(filepath.Join(dir, "movies/A"), 0o755)
	os.WriteFile(filepath.Join(dir, "movies/A/A.mkv"), []byte("old"), 0o644)
	staged := stage(t, dir, "1.part", "new")
	res, err := f.Publish(staged, "movies/A/A.mkv", PolicyReplace, nil, ".trash/b1/movies/A/A.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "movies/A/A.mkv")); string(got) != "new" {
		t.Errorf("final = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, res.TrashedPath)); string(got) != "old" {
		t.Errorf("trash = %q", got)
	}
}

// Many goroutines publish different content to the same destination at once.
// Exactly one must win; all others must get ErrConflict; nothing is lost.
func TestPublishConcurrentSingleWinner(t *testing.T) {
	f, dir := newFS(t)
	const n = 32
	var staged []string
	for i := range n {
		staged = append(staged, stage(t, dir, fmt.Sprintf("%d.part", i), fmt.Sprint(i)))
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, conflicts := 0, 0
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := f.Publish(staged[i], "movies/Race/Race.mkv", PolicyNone, nil, "")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrConflict):
				conflicts++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 || conflicts != n-1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
	left, _ := os.ReadDir(filepath.Join(dir, ".incoming"))
	if len(left) != n-1 {
		t.Fatalf("losing staged files must be kept, have %d", len(left))
	}
}

func TestPublishRejectsNonRegularStaged(t *testing.T) {
	f, dir := newFS(t)
	os.Symlink("/etc/passwd", filepath.Join(dir, ".incoming", "link.part"))
	if _, err := f.Publish(".incoming/link.part", "movies/X/X.mkv", PolicyNone, nil, ""); err == nil {
		t.Fatal("symlink staged file must be rejected")
	}
}

func TestEnsureDirOverFile(t *testing.T) {
	f, dir := newFS(t)
	os.WriteFile(filepath.Join(dir, "movies/file"), nil, 0o644)
	if _, err := f.EnsureDir("movies/file/sub", true); err == nil {
		t.Fatal("expected error")
	}
}

func TestFreeBytes(t *testing.T) {
	f, _ := newFS(t)
	if f.FreeBytes(".incoming") <= 0 {
		t.Fatal("free bytes should be positive")
	}
	same, err := f.SameDevice(".incoming", "movies")
	if err != nil || !same {
		t.Fatalf("same=%v err=%v", same, err)
	}
}

// As root (CI container), published files and new directories get PUID/PGID.
func TestPublishChownAsRoot(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs root; run the Linux test container")
	}
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".incoming"), 0o755)
	f, err := Open(dir, Perms{UID: 1234, GID: 5678, FileMode: 0o640, DirMode: 0o750}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	staged := stage(t, dir, "1.part", "x")
	res, err := f.Publish(staged, "movies/A/A.mkv", PolicyNone, nil, "")
	if err != nil || res.PermWarning != "" {
		t.Fatalf("err=%v warn=%q", err, res.PermWarning)
	}
	for p, mode := range map[string]os.FileMode{"movies/A/A.mkv": 0o640, "movies/A": 0o750, "movies": 0o750} {
		fi, _ := os.Stat(filepath.Join(dir, p))
		st := fi.Sys().(*syscall.Stat_t)
		if st.Uid != 1234 || st.Gid != 5678 || fi.Mode().Perm() != mode {
			t.Errorf("%s: uid=%d gid=%d mode=%v", p, st.Uid, st.Gid, fi.Mode().Perm())
		}
	}
}

// As an unprivileged user, chown to someone else fails: the file is still
// published with the right mode, and a warning is reported.
func TestPublishChownWarningUnprivileged(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("must run unprivileged")
	}
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".incoming"), 0o755)
	f, _ := Open(dir, Perms{UID: os.Getuid() + 1, GID: -1, FileMode: 0o644, DirMode: 0o755}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer f.Close()
	staged := stage(t, dir, "1.part", "x")
	res, err := f.Publish(staged, "movies/A/A.mkv", PolicyNone, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.PermWarning == "" {
		t.Fatal("expected a chown warning")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "movies/A/A.mkv")); fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}
