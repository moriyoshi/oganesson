package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/moriyoshi/oganesson/internal/nativegen"
)

// TestUserReplaces: the project's own go.mod replace directives are
// propagated to the generated module (Go ignores a dependency's replaces). A
// filesystem replacement (no version) is resolved against the og/ directory;
// a versioned replacement is kept verbatim; and directives that would shadow
// og, its TypeScript submodule or the project module are dropped.
func TestUserReplaces(t *testing.T) {
	proj := &nativegen.Project{
		Dir:        "/x/app/og",
		ModulePath: "example.com/app/og",
		Replaces: []nativegen.Replace{
			{OldPath: "example.com/lib", NewPath: "../lib"},                                                       // relative filesystem path
			{OldPath: "example.com/abs", NewPath: "/opt/abs"},                                                     // absolute filesystem path
			{OldPath: "example.com/pin", OldVersion: "v1.2.3", NewPath: "example.com/fork", NewVersion: "v1.2.4"}, // versioned
			{OldPath: "github.com/moriyoshi/oganesson", NewPath: "../og-checkout"},                                // dropped (og writes its own)
			{OldPath: "github.com/microsoft/TypeScript/tsc", NewPath: "../tsc"},                                   // dropped
			{OldPath: "example.com/app/og", NewPath: "../self"},                                                   // dropped (the project module)
		},
	}
	got := userReplaces(proj)
	want := []string{
		"example.com/lib => " + filepath.FromSlash("/x/app/lib"),
		"example.com/abs => " + filepath.FromSlash("/opt/abs"),
		"example.com/pin v1.2.3 => example.com/fork v1.2.4",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("userReplaces:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestBuildNativeApp: og build -o of a project with native Go modules
// (testdata/apps/next-native: og:hash, og:clock in og/) builds the app
// binary with the Go toolchain against this checkout, prerenders the static
// page in that binary (so the page's Go call ran at build time), serves the
// route handler's synchronous, []byte and asynchronous calls, and writes the
// modules' types into og-env.d.ts.
func TestBuildNativeApp(t *testing.T) {
	if testing.Short() {
		t.Skip("builds an app binary with the Go toolchain")
	}
	project, _ := filepath.Abs("../../testdata/apps/next-native")
	if _, err := os.Stat(filepath.Join(project, "node_modules", "next")); err != nil {
		t.Skip("fixture not installed: go run ./cmd/og install testdata/apps/next-native")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no Go toolchain on PATH")
	}
	source, _ := filepath.Abs("../..")
	if _, err := os.Stat(filepath.Join(source, "third_party", "typescript", "tsc")); err != nil {
		t.Skip("third_party/typescript not synced")
	}
	exe := filepath.Join(t.TempDir(), "app")
	if err := runBuild(context.Background(), []string{"-o", exe, "-og-source", source, project}); err != nil {
		t.Fatal(err)
	}

	// The digest of "oganesson", computed by og:hash while prerendering.
	const digest = "8b035693c7fb15d9c010efacd222d793d4d3cde5d6016f4b3d7e904798d9a304"
	b, err := os.ReadFile(filepath.Join(project, ".og", "prerender.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pre struct {
		Entries map[string]struct{ Body []byte } `json:"entries"`
	}
	if err := json.Unmarshal(b, &pre); err != nil {
		t.Fatal(err)
	}
	if home, ok := pre.Entries["/"]; !ok || !strings.Contains(string(home.Body), digest) {
		t.Errorf("/ was not prerendered with og:hash (entry present: %v)", ok)
	}

	types, err := os.ReadFile(filepath.Join(project, "og-env.d.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"declare module \"og:hash\"",
		"export function sha256(s: string): Digest;",
		"export function raw(b: Uint8Array): Uint8Array;",
		"export function after(t: Date, ms: number): Promise<Date>;",
	} {
		if !strings.Contains(string(types), want) {
			t.Errorf("og-env.d.ts lacks %q", want)
		}
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	cmd := exec.Command(exe, "-addr", addr)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	get := func(path string) string {
		t.Helper()
		var lastErr error
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			res, err := http.Get("http://" + addr + path)
			if err != nil {
				lastErr = err
				continue
			}
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET %s: %d %s", path, res.StatusCode, body)
			}
			return string(body)
		}
		t.Fatalf("GET %s: %v", path, lastErr)
		return ""
	}
	if home := get("/"); !strings.Contains(home, digest) {
		t.Errorf("/ lacks the digest:\n%.500s", home)
	}
	for path, want := range map[string]string{
		"/api/digest?s=abc": `{"digest":{"algorithm":"sha256","hex":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad","size":3},"rawLength":32,"rawIsBytes":true,"later":"2026-01-01T00:00:00.005Z"}`,
		"/api/digest":       `{"digest":{"error":"nothing to digest"},"rawLength":32,"rawIsBytes":true,"later":"2026-01-01T00:00:00.005Z"}`,
	} {
		if got := get(path); got != want {
			t.Errorf("GET %s:\ngot  %s\nwant %s", path, got, want)
		}
	}
}

// TestBuildNativeCrossPrerender: og build -o for a non-host target of a
// project with native Go modules still prerenders static pages with the
// project's Go functions. The target binary cannot run on the build host, so
// og builds a throwaway host binary only to prerender; without it the page's
// og:hash call would be missing from prerender.json (the in-process
// prerenderer has no native modules).
func TestBuildNativeCrossPrerender(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-builds an app binary with the Go toolchain")
	}
	project, _ := filepath.Abs("../../testdata/apps/next-native")
	if _, err := os.Stat(filepath.Join(project, "node_modules", "next")); err != nil {
		t.Skip("fixture not installed: go run ./cmd/og install testdata/apps/next-native")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no Go toolchain on PATH")
	}
	source, _ := filepath.Abs("../..")
	if _, err := os.Stat(filepath.Join(source, "third_party", "typescript", "tsc")); err != nil {
		t.Skip("third_party/typescript not synced")
	}
	// A target that is not the host, so og must build a separate host binary
	// to prerender (pure Go, CGO off, so the cross build itself succeeds).
	goarch := "amd64"
	if runtime.GOARCH == "amd64" {
		goarch = "arm64"
	}
	target := runtime.GOOS + "/" + goarch
	exe := filepath.Join(t.TempDir(), "app")
	if err := runBuild(context.Background(), []string{"-o", exe, "-target", target, "-og-source", source, project}); err != nil {
		t.Fatal(err)
	}

	// The digest of "oganesson", computed by og:hash while prerendering.
	const digest = "8b035693c7fb15d9c010efacd222d793d4d3cde5d6016f4b3d7e904798d9a304"
	b, err := os.ReadFile(filepath.Join(project, ".og", "prerender.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pre struct {
		Entries map[string]struct{ Body []byte } `json:"entries"`
	}
	if err := json.Unmarshal(b, &pre); err != nil {
		t.Fatal(err)
	}
	if home, ok := pre.Entries["/"]; !ok || !strings.Contains(string(home.Body), digest) {
		t.Errorf("/ was not prerendered with og:hash on a cross build (entry present: %v)", ok)
	}
}

// TestEjectNatives: an ejected module registers the project's native Go
// modules in natives_gen.go (regenerated on every eject, removed when the
// project no longer has any), and a new go.mod requires and replaces the
// project's og/ module.
func TestEjectNatives(t *testing.T) {
	project := t.TempDir()
	for name, src := range map[string]string{
		"og/go.mod":     "module example.com/p/og\n\ngo 1.27.0\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib => ../lib\n",
		"og/hash/a.go":  "package hash\n\nfunc Sum(s string) string { return s }\n",
		"package.json":  `{"name":"p"}`,
		"og/hash/a2.go": "package hash\n\n//og:async\nfunc Later() error { return nil }\n",
	} {
		p := filepath.Join(project, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	natives, err := nativegen.Scan(project)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "ejected")
	files := map[string][]byte{"server.js": []byte("v1")}
	if err := eject(out, project, files, "", false, natives); err != nil {
		t.Fatal(err)
	}
	gomod, _ := os.ReadFile(filepath.Join(out, "go.mod"))
	for _, want := range []string{
		"require example.com/p/og v0.0.0-00010101000000-000000000000\n",
		"replace example.com/p/og => " + filepath.Join(project, "og") + "\n",
		// Propagated from og/go.mod, its relative path resolved against og/.
		"replace example.com/lib => " + filepath.Join(project, "lib") + "\n",
	} {
		if !strings.Contains(string(gomod), want) {
			t.Errorf("go.mod lacks %q:\n%s", want, gomod)
		}
	}
	glue, err := os.ReadFile(filepath.Join(out, nativeGlueFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"package main\n",
		`og_hash "example.com/p/og/hash"`,
		`{Name: "sum", Fn: og_hash.Sum, Async: false}`,
		`{Name: "later", Fn: og_hash.Later, Async: true}`,
	} {
		if !strings.Contains(string(glue), want) {
			t.Errorf("%s lacks %q:\n%s", nativeGlueFile, want, glue)
		}
	}
	// Without native modules, the generated file goes; the user's stay.
	if err := os.WriteFile(filepath.Join(out, "mine.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := eject(out, project, files, "", false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, nativeGlueFile)); !os.IsNotExist(err) {
		t.Errorf("%s kept without native modules (%v)", nativeGlueFile, err)
	}
	if _, err := os.Stat(filepath.Join(out, "mine.go")); err != nil {
		t.Error(err)
	}
}
