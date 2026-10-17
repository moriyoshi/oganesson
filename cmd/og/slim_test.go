package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSlimRuntimeReason: og build -o of an ordinary (no native module) app
// packages a slim serve-only runtime when it can, and otherwise copies the
// whole og. The decision needs the Go toolchain, and an unpublished og (a
// pseudo-version) can resolve its own module only from a checkout.
func TestSlimRuntimeReason(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no Go toolchain on PATH")
	}
	// ogModuleVersion reads the package global; restore it after the cases.
	saved := version
	t.Cleanup(func() { version = saved })

	// A published og resolves its module from the proxy, so no checkout is
	// needed: slim is available even without a source.
	version = "v1.2.3"
	if got := slimRuntimeReason("", "go"); got != "" {
		t.Errorf("published og, no source: reason %q, want none", got)
	}
	// An unpublished og (pseudo-version) with no checkout cannot: fall back.
	version = ""
	if got := slimRuntimeReason("", "go"); !strings.Contains(got, "unpublished") {
		t.Errorf("unpublished og, no source: reason %q, want one about an unpublished build", got)
	}
	// ...but a checkout lets even an unpublished og build a slim runtime.
	if got := slimRuntimeReason("/some/oganesson/checkout", "go"); got != "" {
		t.Errorf("unpublished og, with source: reason %q, want none", got)
	}
	// An overridden toolchain that does not resolve is reported (and takes
	// precedence over the other checks), so og falls back to the full og.
	version = "v1.2.3"
	if got := slimRuntimeReason("", "/no/such/go-toolchain-xyz"); !strings.Contains(got, "Go toolchain") {
		t.Errorf("missing -go toolchain: reason %q, want one about the Go toolchain", got)
	}
}

// TestBuildSlimApp: og build -o of an ordinary app (testdata/apps/next-hello,
// no og/ native modules) packages the slim serve-only runtime, not the whole
// og. The produced binary serves the app, and it does not carry the
// TypeScript compiler the full og links: tsc embeds the lib.*.d.ts files, so
// their name appears verbatim in a full-og binary but not in a slim one (the
// app payload is DEFLATE-compressed, so the marker can only surface from an
// uncompressed base executable).
func TestBuildSlimApp(t *testing.T) {
	if testing.Short() {
		t.Skip("builds an app binary with the Go toolchain")
	}
	project, _ := filepath.Abs("../../testdata/apps/next-hello")
	if _, err := os.Stat(filepath.Join(project, "node_modules", "next")); err != nil {
		t.Skip("fixture not installed: go run ./cmd/og install testdata/apps/next-hello")
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

	bin, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bin, []byte("lib.dom.d.ts")) {
		t.Errorf("the app binary carries the TypeScript compiler (lib.dom.d.ts present): it packaged the full og, not a slim runtime")
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
	var lastErr error
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		res, err := http.Get("http://" + addr + "/about")
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET /about: %d %s", res.StatusCode, body)
		}
		if !strings.Contains(string(body), "A static page") {
			t.Errorf("GET /about lacks the prerendered text:\n%.500s", body)
		}
		return
	}
	t.Fatalf("GET /about: %v", lastErr)
}
