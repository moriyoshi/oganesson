package jshost

import (
	"context"
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

// lightningcssLoader is the shape of lightningcss/node/index.js that
// nativeSubstitute rewrites: the lightningcss-<platform> package require
// (line 18 of the real file) and its catch-branch .node fallback, both
// inside one try/catch. The platform branch is trimmed to avoid a
// detect-libc require; the two literal requires are verbatim.
const lightningcssLoader = "let parts = [process.platform, process.arch];\n" +
	"if (process.platform === 'linux') { parts.push('gnu'); }\n" +
	"let native;\n" +
	"try {\n" +
	"  native = require(`lightningcss-${parts.join('-')}`);\n" +
	"} catch (err) {\n" +
	"  native = require(`../lightningcss.${parts.join('-')}.node`);\n" +
	"}\n" +
	"module.exports = native;\n"

func writeLightningcssLoader(t *testing.T, dir string) {
	t.Helper()
	node := filepath.Join(dir, "node_modules", "lightningcss", "node")
	if err := os.MkdirAll(node, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]string{
		"package.json":  `{"name":"lightningcss","version":"1.32.0","main":"node/index.js"}`,
		"node/index.js": lightningcssLoader,
	} {
		p := filepath.Join(dir, "node_modules", "lightningcss", filepath.FromSlash(name))
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestLightningcssNativeSubstitute: at build time lightningcss's loader is
// rewritten to its lightningcss-wasm build. The whole try/catch is
// rewritten, not just the package require: when lightningcss-wasm is
// present, the module is it (the .node fallback never runs); when it is
// missing (a node_modules populated by npm/pnpm/yarn, not og install), the
// catch branch throws an actionable error naming lightningcss-wasm and
// og install, NOT the opaque ERR_DLOPEN_FAILED of the native .node.
func TestLightningcssNativeSubstitute(t *testing.T) {
	// With lightningcss-wasm: require("lightningcss") is its exports.
	withWasm := t.TempDir()
	writeLightningcssLoader(t, withWasm)
	wasm := filepath.Join(withWasm, "node_modules", "lightningcss-wasm")
	if err := os.MkdirAll(wasm, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]string{
		"package.json": `{"name":"lightningcss-wasm","version":"1.32.0","main":"index.js"}`,
		"index.js":     `module.exports = { __fromWasm: true };`,
	} {
		if err := os.WriteFile(filepath.Join(wasm, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := &Host{Dir: withWasm, Out: io.Discard}
	code, err := h.Bundle("", `
const req = eval("require");
globalThis.result = String(req("lightningcss").__fromWasm === true);
`, "")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := h.Eval(context.Background(), code, "result"); err != nil || v != "true" {
		t.Fatalf("with lightningcss-wasm: got %v, err %v", v, err)
	}

	// Without lightningcss-wasm: an actionable error, and never the .node's
	// ERR_DLOPEN_FAILED. The caught lightningcss-wasm miss is kept as cause.
	noWasm := t.TempDir()
	writeLightningcssLoader(t, noWasm)
	h = &Host{Dir: noWasm, Out: io.Discard}
	code, err = h.Bundle("", `
const req = eval("require");
let out;
try { req("lightningcss"); out = "no throw"; }
catch (e) { out = [e.message, String(e.code), e.cause ? "has-cause" : "no-cause"].join("|"); }
globalThis.result = out;
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "result")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := v.(string)
	if strings.Contains(s, "ERR_DLOPEN_FAILED") {
		t.Errorf("surfaced the opaque native-addon error: %q", s)
	}
	for _, want := range []string{"lightningcss-wasm", "og install", "has-cause"} {
		if !strings.Contains(s, want) {
			t.Errorf("error lacks %q: %q", want, s)
		}
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
