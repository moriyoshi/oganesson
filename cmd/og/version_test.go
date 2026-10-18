package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestOgVersionPrefersInjected(t *testing.T) {
	defer func(v string) { version = v }(version)
	version = "v1.2.3"
	if got := ogVersion(); got != "v1.2.3" {
		t.Errorf("ogVersion() = %q, want %q", got, "v1.2.3")
	}
}

func TestOgVersionFallsBack(t *testing.T) {
	defer func(v string) { version = v }(version)
	version = ""
	// Without an injected version, ogVersion falls back to the Go-stamped
	// module version (or "unknown"); either way it must be non-empty.
	if got := ogVersion(); got == "" {
		t.Error("ogVersion() is empty with no injected version")
	}
}

func TestWriteVersion(t *testing.T) {
	defer func(v string) { version = v }(version)
	version = "v9.9.9"
	var b strings.Builder
	if err := writeVersion(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"og v9.9.9", runtime.GOOS + "/" + runtime.GOARCH, runtime.Version()} {
		if !strings.Contains(out, want) {
			t.Errorf("writeVersion output %q lacks %q", out, want)
		}
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("writeVersion output %q not newline-terminated", out)
	}
}
