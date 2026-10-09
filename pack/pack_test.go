package pack

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTripAndRepack(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "base")
	if err := os.WriteFile(exe, []byte("#!fake executable bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(dst string, files map[string][]byte, from string) {
		var buf bytes.Buffer
		if err := Write(&buf, from, files); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, buf.Bytes(), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Open(exe); err != ErrNoPayload {
		t.Fatalf("plain executable: err = %v", err)
	}
	app1 := filepath.Join(dir, "app1")
	write(app1, map[string][]byte{"server.js": []byte("one"), "meta.json": []byte("{}")}, exe)
	fsys, err := Open(app1)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := fs.ReadFile(fsys, "server.js"); string(b) != "one" {
		t.Fatalf("server.js = %q", b)
	}
	// Repacking from a packed binary replaces, not stacks, the payload.
	app2 := filepath.Join(dir, "app2")
	write(app2, map[string][]byte{"server.js": []byte("two")}, app1)
	fsys, err = Open(app2)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := fs.ReadFile(fsys, "server.js"); string(b) != "two" {
		t.Fatalf("server.js = %q", b)
	}
	if _, err := fs.ReadFile(fsys, "meta.json"); err == nil {
		t.Fatal("old payload leaked into the repacked binary")
	}
	b, _ := os.ReadFile(app2)
	if !bytes.HasPrefix(b, []byte("#!fake executable bytes")) {
		t.Fatal("executable prefix lost")
	}
}
