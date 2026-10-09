package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteExecutable: an app binary replaces the old one as a new file
// (macOS kills a signed binary modified in place), executable, with no
// temporary file left behind.
func TestWriteExecutable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app")
	if err := os.WriteFile(p, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeExecutable(p, []byte("new")); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Error("the binary was rewritten in place")
	}
	if b, _ := os.ReadFile(p); string(b) != "new" || after.Mode().Perm() != 0o755 {
		t.Errorf("contents %q, mode %v", b, after.Mode().Perm())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("left %d files", len(entries))
	}
}
