package atomicfile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// leftovers is every file in dir but keep.
func leftovers(t *testing.T, dir, keep string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.Name() != keep {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestWriteReplacesTheFileWhole(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "evado")
	path := filepath.Join(dir, "todos.json")
	if err := Write(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("second, and longer")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second, and longer" {
		t.Errorf("file = %q", got)
	}
	if left := leftovers(t, dir, "todos.json"); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
	if runtime.GOOS == "windows" {
		return // no Unix modes there
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("directory mode = %o, want no group or other access", perm)
	}
}

// A save that cannot be put in place leaves what was there and no
// temporary file to pile up beside it.
func TestAFailedWriteLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	// A directory where the file should be: the rename fails on every
	// platform, after the data was written.
	path := filepath.Join(dir, "todos.json")
	if err := os.MkdirAll(filepath.Join(path, "inside"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new")); err == nil {
		t.Fatal("a write over a directory succeeded")
	}
	if left := leftovers(t, dir, "todos.json"); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		t.Errorf("what was there changed: %v %v", fi, err)
	}
}
