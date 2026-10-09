package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moriyoshi/oganesson/runtime/notices"
)

func TestEject(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "package.json"), []byte(`{"name":"@Acme/My App"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	source, _ := filepath.Abs("../..") // this checkout
	out := filepath.Join(t.TempDir(), "ejected")
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(out, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	files := map[string][]byte{"server.js": []byte("v1"), "_next/static/a.js": []byte("a"), "stale.txt": []byte("x")}
	if err := eject(out, project, files, source, false, nil); err != nil {
		t.Fatal(err)
	}
	gomod := read("go.mod")
	for _, want := range []string{
		"module acme-my-app\n",
		"github.com/moriyoshi/oganesson => " + source + "\n",
		"github.com/microsoft/TypeScript/tsc => " + source + "/third_party/typescript/tsc\n",
	} {
		if !strings.Contains(gomod, want) {
			t.Errorf("go.mod lacks %q:\n%s", want, gomod)
		}
	}
	if got := read("app/_next/static/a.js"); got != "a" {
		t.Errorf("app/_next/static/a.js = %q", got)
	}
	if main := read("main.go"); !strings.Contains(main, "//go:embed all:app") || !strings.Contains(main, "app.Serve(ctx, fsys, *addr, wrap)") ||
		!strings.Contains(main, `flag.Bool("licenses"`) || !strings.Contains(main, "notices.Write(os.Stdout, fsys)") {
		t.Errorf("main.go:\n%s", main)
	}
	if read("THIRD_PARTY_NOTICES.txt") != notices.Text {
		t.Error("THIRD_PARTY_NOTICES.txt is not the embedded notices")
	}
	if err := os.WriteFile(filepath.Join(out, "THIRD_PARTY_NOTICES.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Re-ejecting replaces the app and keeps the user's Go files.
	if err := os.WriteFile(filepath.Join(out, "wrap.go"), []byte("package main // mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := eject(out, project, map[string][]byte{"server.js": []byte("v2"), notices.AppFile: []byte("app npm notices\n")}, source, true, nil); err != nil {
		t.Fatal(err)
	}
	if read("app/server.js") != "v2" || read("wrap.go") != "package main // mine\n" {
		t.Error("re-eject did not replace the app or did not keep wrap.go")
	}
	if got := read("THIRD_PARTY_NOTICES.txt"); !strings.HasPrefix(got, notices.Text) || !strings.HasSuffix(got, "\napp npm notices\n") {
		t.Error("re-eject did not refresh THIRD_PARTY_NOTICES.txt with og's and the app's notices")
	}
	if _, err := os.Stat(filepath.Join(out, "app/stale.txt")); !os.IsNotExist(err) {
		t.Error("stale app file kept")
	}
	if !strings.Contains(read("README.md"), "-readable") {
		t.Error("README does not mention the readable build")
	}

	// Without -og-source, the replace lines are instructions.
	plain := filepath.Join(t.TempDir(), "plain")
	if err := eject(plain, project, files, "", false, nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(plain, "go.mod"))
	if strings.Contains(string(b), "\nreplace (") || !strings.Contains(string(b), "// replace (") {
		t.Errorf("go.mod without source:\n%s", b)
	}

	// The version injected into main.version is pinned; one go.mod would
	// reject is not.
	defer func(v string) { version = v }(version)
	for _, tc := range []struct{ injected, want string }{
		{"v1.2.3", "v1.2.3"},
		{"v1.2.3-rc.1", "v1.2.3-rc.1"},
		{"v1.2", "v0.0.0-00010101000000-000000000000"},
		{"1.2.3", "v0.0.0-00010101000000-000000000000"},
		{"v1.2.3+dirty", "v0.0.0-00010101000000-000000000000"},
	} {
		version = tc.injected
		pinned := filepath.Join(t.TempDir(), "pinned")
		if err := eject(pinned, project, files, "", false, nil); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(filepath.Join(pinned, "go.mod"))
		if !strings.Contains(string(b), "\nrequire github.com/moriyoshi/oganesson "+tc.want+"\n") {
			t.Errorf("version %q: go.mod does not require %s:\n%s", tc.injected, tc.want, b)
		}
	}

	// Refusals.
	busy := t.TempDir()
	_ = os.WriteFile(filepath.Join(busy, "notes.txt"), []byte("x"), 0o644)
	if err := eject(busy, project, files, "", false, nil); err == nil {
		t.Error("ejected into a non-empty directory that is not a module")
	}
	if err := eject(filepath.Join(t.TempDir(), "x"), project, files, t.TempDir(), false, nil); err == nil {
		t.Error("accepted an -og-source without go.mod")
	}
}
