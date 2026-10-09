package licenses

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func metafile(t *testing.T, keys ...string) string {
	t.Helper()
	inputs := map[string]any{}
	for _, k := range keys {
		inputs[k] = map[string]any{"bytes": 1}
	}
	b, err := json.Marshal(map[string]any{"inputs": inputs})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCollect(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"node_modules/plain/package.json":                    `{"name":"plain","version":"1.0.0","license":"MIT"}`,
		"node_modules/plain/LICENSE":                         "plain license text\n",
		"node_modules/plain/dist/package.json":               `{"type":"module"}`, // unnamed: not a package
		"node_modules/plain/dist/index.js":                   "",
		"node_modules/@scope/pkg/package.json":               `{"name":"@scope/pkg","version":"2.0.0","license":{"type":"ISC"}}`,
		"node_modules/@scope/pkg/LICENSE.md":                 "scoped license text\n",
		"node_modules/@scope/pkg/lib/a.js":                   "",
		"node_modules/nolicense/package.json":                `{"name":"nolicense","version":"0.1.0","licenses":[{"type":"MIT"},{"type":"Apache-2.0"}]}`,
		"node_modules/nolicense/index.js":                    "",
		"node_modules/unknown/package.json":                  `{"name":"unknown","version":"3.0.0"}`,
		"node_modules/unknown/index.js":                      "",
		"node_modules/host/package.json":                     `{"name":"host","version":"9.0.0","license":"MIT"}`,
		"node_modules/host/LICENSE":                          "host license text\n",
		"node_modules/host/dist/compiled/inner/package.json": `{"name":"inner","version":"5.0.0","license":"BSD-3-Clause"}`,
		"node_modules/host/dist/compiled/inner/LICENSE":      "inner license text\n",
		"node_modules/host/dist/compiled/inner/cjs/x.js":     "",
		"node_modules/host/dist/server.js":                   "",
		"node_modules/a/node_modules/plain/package.json":     `{"name":"plain","version":"1.0.0","license":"MIT"}`,
		"node_modules/a/node_modules/plain/LICENSE":          "duplicate plain\n",
		"node_modules/a/node_modules/plain/index.js":         "",
		"src/app.js": "",
	})
	c := &Collector{}
	meta := metafile(t,
		"node_modules/plain/dist/index.js",                             // relative to workDir
		"rsc:"+filepath.Join(root, "node_modules/@scope/pkg/lib/a.js"), // plugin namespace
		"vite:\x00"+filepath.Join(root, "node_modules/nolicense/index.js")+"?commonjs-proxy",
		"node_modules/unknown/index.js",
		"node_modules/host/dist/compiled/inner/cjs/x.js", // vendored copy: its own package
		"node_modules/host/dist/server.js",
		"node_modules/a/node_modules/plain/index.js", // same name@version installed twice
		"src/app.js",      // the app's own code
		"og:client-entry", // virtual
		"<stdin>",
	)
	if err := c.AddMetafile(meta, root); err != nil {
		t.Fatal(err)
	}
	text, err := c.Text()
	if err != nil {
		t.Fatal(err)
	}
	got := string(text)
	sections := strings.Split(got, strings.Repeat("=", 80)+"\n")[1:]
	var titles []string
	for _, s := range sections {
		titles = append(titles, strings.SplitN(s, "\n", 2)[0])
	}
	want := []string{
		"@scope/pkg 2.0.0 (ISC)",
		"host 9.0.0 (MIT)",
		"inner 5.0.0 (BSD-3-Clause)",
		"nolicense 0.1.0 (MIT OR Apache-2.0)",
		"plain 1.0.0 (MIT)",
		"unknown 3.0.0",
	}
	if strings.Join(titles, "|") != strings.Join(want, "|") {
		t.Errorf("sections:\n  %s\nwant:\n  %s", strings.Join(titles, "\n  "), strings.Join(want, "\n  "))
	}
	for _, s := range []string{
		"--- LICENSE\n\nplain license text\n",
		"--- LICENSE.md\n\nscoped license text\n",
		"--- LICENSE\n\ninner license text\n",
		"The package ships no license file; its package.json declares MIT OR Apache-2.0.\n",
		"The package ships no license file and declares no license.\n",
	} {
		if !strings.Contains(got, s) {
			t.Errorf("text lacks %q", s)
		}
	}
	// The duplicate keeps the shallower copy (sorted by directory).
	if strings.Contains(got, "duplicate plain") {
		t.Error("the nested duplicate of plain@1.0.0 was rendered")
	}

	// Deterministic: a second collector fed in another order renders the same.
	c2 := &Collector{}
	for _, f := range []string{"node_modules/a/node_modules/plain/index.js", "node_modules/unknown/index.js", "node_modules/host/dist/server.js", "node_modules/host/dist/compiled/inner/cjs/x.js", "node_modules/plain/dist/index.js"} {
		c2.AddFile(filepath.Join(root, f))
	}
	c2.AddFile(filepath.Join(root, "node_modules/@scope/pkg/lib/a.js"))
	c2.AddFile(filepath.Join(root, "node_modules/nolicense/index.js"))
	if text2, _ := c2.Text(); string(text2) != got {
		t.Error("rendering depends on the order packages were added")
	}
}

func TestAddNotice(t *testing.T) {
	// A nil collector ignores notices.
	var nilC *Collector
	nilC.AddNotice("X", "body")
	if b, _ := nilC.Text(); b != nil {
		t.Errorf("nil collector Text = %q", b)
	}

	// A notice with no packages still renders (not nil), and dedups by name.
	c := &Collector{}
	c.AddNotice("Google Fonts (downloaded at build time)", "under open font licenses")
	c.AddNotice("Google Fonts (downloaded at build time)", "DIFFERENT BODY, must not appear")
	got, err := c.Text()
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.Contains(s, "\nGoogle Fonts (downloaded at build time)\n\nunder open font licenses\n") {
		t.Errorf("notice not rendered:\n%s", s)
	}
	if strings.Contains(s, "DIFFERENT BODY") || strings.Count(s, "Google Fonts (downloaded at build time)") != 1 {
		t.Errorf("notice was not deduplicated by name:\n%s", s)
	}

	// Packages render first, notices after, both sorted.
	root := t.TempDir()
	write(t, root, map[string]string{
		"node_modules/pkg/package.json": `{"name":"pkg","version":"1.0.0","license":"MIT"}`,
		"node_modules/pkg/LICENSE":      "pkg license\n",
	})
	c2 := &Collector{}
	c2.AddFile(filepath.Join(root, "node_modules/pkg/index.js"))
	c2.AddNotice("Google Fonts (downloaded at build time)", "the font notice body")
	got2, err := c2.Text()
	if err != nil {
		t.Fatal(err)
	}
	s2 := string(got2)
	pkgAt := strings.Index(s2, "\npkg 1.0.0 (MIT)\n")
	noticeAt := strings.Index(s2, "\nGoogle Fonts (downloaded at build time)\n")
	if pkgAt < 0 || noticeAt < 0 || noticeAt < pkgAt {
		t.Errorf("want pkg section before the notice; pkgAt=%d noticeAt=%d:\n%s", pkgAt, noticeAt, s2)
	}
}

func TestEmptyAndNil(t *testing.T) {
	var nilC *Collector
	if err := nilC.AddMetafile(`{"inputs":{"node_modules/x/y.js":{}}}`, "/"); err != nil {
		t.Fatal(err)
	}
	nilC.AddFile("/p/node_modules/x/y.js")
	if b, err := nilC.Text(); b != nil || err != nil {
		t.Errorf("nil collector Text = %q, %v", b, err)
	}
	c := &Collector{}
	if err := c.AddMetafile(`{"inputs":{"src/a.js":{}}}`, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if b, _ := c.Text(); b != nil {
		t.Errorf("no packages: Text = %q", b)
	}
	if err := c.AddMetafile("not json", "/"); err == nil {
		t.Error("accepted a malformed metafile")
	}
}
