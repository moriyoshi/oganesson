package next

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBundlerEnv: next.config loads with TURBOPACK as the project's
// `next build` would set it (lib/bundler.js's parseBundlerArgs): "auto"
// without a flag (Turbopack is the default), "1" for --turbopack / --turbo,
// unset for --webpack; a TURBOPACK the script sets is kept.
func TestBundlerEnv(t *testing.T) {
	t.Setenv("TURBOPACK", "")
	for script, want := range map[string]string{
		"next build":                          "auto",
		"next build --webpack":                "",
		"next build --turbopack":              "1",
		"next build --turbo":                  "1",
		"prisma generate && next build":       "auto",
		"npm run gen && next build --webpack": "",
		"TURBOPACK=1 next build":              "1",
		"":                                    "auto",
	} {
		dir := t.TempDir()
		pkg := `{"scripts":{"gen":"true","build":` + jsonStr(script) + `}}`
		if script == "" {
			pkg = `{}`
		}
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := bundlerEnv(dir)["TURBOPACK"]; got != want {
			t.Errorf("%q: TURBOPACK %q, want %q", script, got, want)
		}
	}
}
