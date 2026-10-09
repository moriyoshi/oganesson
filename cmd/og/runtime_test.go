package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// buildHello cross-compiles a tiny program (module example.com/hello).
func buildHello(t *testing.T, goos, goarch string, cgo bool) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":  "module example.com/hello\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "hello")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = dir
	env := append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "GOWORK=off", "GOFLAGS=")
	if cgo {
		env = append(env, "CGO_ENABLED=1")
	} else {
		env = append(env, "CGO_ENABLED=0")
	}
	cmd.Env = env
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}
	return out
}

func TestCheckRuntime(t *testing.T) {
	hello := buildHello(t, "linux", "arm64", false)
	if err := checkRuntime(hello, "linux", "arm64", "example.com/hello", nil); err != nil {
		t.Errorf("matching runtime rejected: %v", err)
	}
	for _, c := range []struct {
		goos, goarch, path, want string
	}{
		{"linux", "arm64", ogMainPath, "not og"},
		{"linux", "amd64", "example.com/hello", "built for linux/arm64, not linux/amd64"},
		{"windows", "arm64", "example.com/hello", "built for linux/arm64, not windows/arm64"},
	} {
		err := checkRuntime(hello, c.goos, c.goarch, c.path, nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s/%s %s: err = %v, want %q", c.goos, c.goarch, c.path, err, c.want)
		}
	}
	if err := checkRuntime(filepath.Join(t.TempDir(), "missing"), "linux", "arm64", ogMainPath, nil); err == nil {
		t.Error("missing file accepted")
	}
}

func TestSameSource(t *testing.T) {
	bi := func(kv ...string) *debug.BuildInfo {
		b := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}
		for i := 0; i+1 < len(kv); i += 2 {
			b.Settings = append(b.Settings, debug.BuildSetting{Key: kv[i], Value: kv[i+1]})
		}
		return b
	}
	for _, c := range []struct {
		a, b *debug.BuildInfo
		want bool
	}{
		{bi("vcs.revision", "abc"), bi("vcs.revision", "abc"), true},
		{bi("vcs.revision", "abc"), bi("vcs.revision", "def"), false},
		{bi("vcs.revision", "abc"), bi("vcs.revision", "abc", "vcs.modified", "true"), false},
		{bi("vcs.revision", "abc"), bi(), true}, // unstamped: the app format guards it
	} {
		if got := sameSource(c.a, c.b); got != c.want {
			t.Errorf("sameSource(%s, %s) = %v", sourceID(c.a), sourceID(c.b), got)
		}
	}
	if got := sourceID(bi()); got != "devel" {
		t.Errorf("sourceID(devel) = %q", got)
	}
}

func TestParseTarget(t *testing.T) {
	if g, a, err := parseTarget("linux/amd64"); err != nil || g != "linux" || a != "amd64" {
		t.Errorf("parseTarget = %s %s %v", g, a, err)
	}
	for _, bad := range []string{"linux", "/amd64", "linux/"} {
		if _, _, err := parseTarget(bad); err == nil {
			t.Errorf("parseTarget(%q) accepted", bad)
		}
	}
	if runtimeFileName("windows", "amd64") != "og-windows-amd64.exe" {
		t.Error("windows runtime name lacks .exe")
	}
}
