package jshost

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLightningcss covers experimental.useLightningcss: Next's
// lightningcss-loader transform (targets from browserslist, nesting always
// lowered, lightningCssFeatures) on lightningcss-wasm, and Next's refusal
// to combine it with PostCSS plugins.
func TestLightningcss(t *testing.T) {
	nextDir := nextHello(t)
	run := func(root string, features map[string]any, css string) (string, error) {
		t.Helper()
		withLightningcssWasm(t, root)
		h := &Host{Dir: root, Out: io.Discard}
		ph, err := h.StartPlugins("", HostOptions{PostCSS: &PostCSSOptions{NextDir: nextDir, Root: root, UseLightningcss: true}, LightningCSSFeatures: features})
		if err != nil {
			t.Fatal(err)
		}
		defer ph.Close()
		return ph.Lightningcss(filepath.Join(root, "a.css"), css)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".browserslistrc"), []byte("safari 13\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	css, err := run(root, nil, ".a { user-select: none; .b { color: red } }")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"-webkit-user-select: none", ".a .b {"} {
		if !strings.Contains(css, w) {
			t.Errorf("output lacks %q:\n%s", w, css)
		}
	}

	// A modern target keeps what it supports, except nesting.
	modern := t.TempDir()
	if err := os.WriteFile(filepath.Join(modern, ".browserslistrc"), []byte("chrome 130\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const lightDark = ".a { user-select: none; .b { color: light-dark(#000, #fff) } }"
	if css, err = run(modern, nil, lightDark); err != nil || strings.Contains(css, "-webkit-") || !strings.Contains(css, ".a .b") || strings.Contains(css, "--lightningcss-light") {
		t.Errorf("chrome 130: %q %v", css, err)
	}
	// lightningCssFeatures.include lowers light-dark() even for that target.
	if css, err = run(modern, map[string]any{"include": []any{"light-dark"}}, lightDark); err != nil || !strings.Contains(css, "--lightningcss-light") {
		t.Errorf("chrome 130 with include light-dark: %q %v", css, err)
	}

	withPlugins := t.TempDir()
	for name, src := range map[string]string{
		"postcss.config.json":               `{"plugins":["noop-plugin"]}`,
		"node_modules/noop-plugin/index.js": `const p = () => ({ postcssPlugin: "noop" }); p.postcss = true; module.exports = p;`,
	} {
		p := filepath.Join(withPlugins, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := run(withPlugins, nil, ".a{}"); err == nil || !strings.Contains(err.Error(), "does not work with postcss plugins") {
		t.Errorf("with PostCSS plugins: err = %v", err)
	}
}

// TestLightningcssMissing: without lightningcss-wasm in the project, the
// build says what to install.
func TestLightningcssMissing(t *testing.T) {
	root := t.TempDir()
	h := &Host{Dir: root, Out: io.Discard}
	ph, err := h.StartPlugins("", HostOptions{PostCSS: &PostCSSOptions{NextDir: nextHello(t), Root: root, UseLightningcss: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer ph.Close()
	if _, err := ph.Lightningcss(filepath.Join(root, "a.css"), ".a{}"); err == nil || !strings.Contains(err.Error(), "add lightningcss-wasm to devDependencies") {
		t.Errorf("err = %v", err)
	}
}

// withLightningcssWasm links the fixture's lightningcss-wasm into root, as
// og install would have installed it.
func withLightningcssWasm(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(nextHello(t), "node_modules/lightningcss-wasm"), filepath.Join(root, "node_modules/lightningcss-wasm")); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
}
