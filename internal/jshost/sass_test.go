package jshost

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSass runs Next's Sass preprocessors: sass-loader (with sassOptions
// and additionalData), then resolve-url-loader rebasing a partial's url().
func TestSass(t *testing.T) {
	nextDir := nextHello(t)
	root := t.TempDir()
	write := func(name, src string) {
		p := filepath.Join(root, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("styles/_mixins.scss", "@mixin bg { background: url(\"./bg.png\"); }\n")
	write("styles/bg.png", "x")
	write("lib/_vars.scss", "$gap: 7px;\n")
	write("app/a.scss", "@use \"../styles/mixins\";\n@use \"vars\";\n.a { @include mixins.bg; margin: vars.$gap; color: $brand; .b { c: d } }\n")
	h := &Host{Dir: root, Out: io.Discard}
	ph, err := h.StartPlugins("", HostOptions{
		PostCSS:     &PostCSSOptions{NextDir: nextDir, Root: root},
		SassOptions: map[string]any{"loadPaths": []any{filepath.Join(root, "lib")}, "additionalData": "$brand: #123456;"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ph.Close()
	css, err := ph.Sass(filepath.Join(root, "app/a.scss"))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{`url("../styles/bg.png")`, "margin:7px", "color:#123456", ".a .b{c:d}"} {
		if !strings.Contains(css, w) {
			t.Errorf("output lacks %s:\n%s", w, css)
		}
	}
}

// TestSassLiveOptions: sassOptions values that do not survive JSON (a custom
// importer here) reach sass-loader from the project's next.config, which the
// build host evaluates itself (function form included).
func TestSassLiveOptions(t *testing.T) {
	nextDir := nextHello(t)
	root := t.TempDir()
	files := map[string]string{
		"next.config.mjs": `export default (phase) => ({
  sassOptions: {
    importers: [{
      canonicalize: (url) => (url.startsWith("virtual:") ? new URL(url) : null),
      load: () => ({ contents: "$gap: 9px; $phase: '" + phase + "';", syntax: "scss" }),
    }],
  },
});
`,
		"a.scss": "@use \"virtual:vars\" as v;\n.a { margin: v.$gap; content: v.$phase; }\n",
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := &Host{Dir: root, Out: io.Discard}
	ph, err := h.StartPlugins("", HostOptions{
		PostCSS:        &PostCSSOptions{NextDir: nextDir, Root: root},
		NextConfigFile: filepath.Join(root, "next.config.mjs"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ph.Close()
	css, err := ph.Sass(filepath.Join(root, "a.scss"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(css, "margin:9px") || !strings.Contains(css, `content:"`+PhaseProductionBuild+`"`) {
		t.Errorf("custom importer not applied:\n%s", css)
	}
}
