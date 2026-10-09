package jshost

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nextHello(t *testing.T) string {
	t.Helper()
	dir, _ := filepath.Abs("../../testdata/apps/next-hello")
	if _, err := os.Stat(filepath.Join(dir, "node_modules/next")); err != nil {
		t.Skip("run: go run ./cmd/og install testdata/apps/next-hello")
	}
	return dir
}

// TestNextFontGoogle runs next/font/google through Next's own loader and
// postcss-next-font, with Google's responses mocked the way Next's tests do
// (NEXT_FONT_GOOGLE_MOCKED_RESPONSES).
func TestNextFontGoogle(t *testing.T) {
	dir := nextHello(t)
	font := filepath.Join(dir, "node_modules/next/dist/next-devtools/server/font/geist-latin.woff2")
	mock := filepath.Join(t.TempDir(), "mock.js")
	css := "/* latin */\n@font-face {\n  font-family: 'Inter';\n  font-style: normal;\n  font-weight: 100 900;\n  font-display: swap;\n  src: url(" + font + ") format('woff2');\n  unicode-range: U+0000-00FF;\n}\n"
	if err := os.WriteFile(mock, []byte("module.exports = new Proxy({}, { get: (_, url) => (globalThis.__requested = url, "+jsonString(css)+") });"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{Dir: dir, Out: io.Discard, Env: map[string]string{"NEXT_FONT_GOOGLE_MOCKED_RESPONSES": mock}}
	ph, err := h.StartPlugins("", HostOptions{PostCSS: &PostCSSOptions{NextDir: dir, Root: dir}})
	if err != nil {
		t.Fatal(err)
	}
	defer ph.Close()
	r, err := ph.NextFont(map[string]any{
		"loader": "next/font/google", "path": "app/page.tsx", "import": "Inter",
		"arguments": []any{map[string]any{"subsets": []any{"latin"}, "variable": "--font-inter"}}, "variableName": "inter",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Files) != 1 || !strings.HasSuffix(r.Files[0].Name, "-s.p.woff2") || len(r.Files[0].Data) == 0 {
		t.Fatalf("files = %+v", r.Files)
	}
	for _, w := range []string{
		"font-family: 'Inter'", "url(/_next/" + r.Files[0].Name + ")",
		"font-family: 'Inter Fallback'", `src: local("Arial")`, "size-adjust:",
		".__className_", ".__variable_", "--font-inter: 'Inter', 'Inter Fallback'",
	} {
		if !strings.Contains(r.CSS, w) {
			t.Errorf("CSS lacks %q:\n%s", w, r.CSS)
		}
	}
	names := []string{}
	for _, e := range r.Exports {
		names = append(names, e[0].(string))
	}
	if strings.Join(names, ",") != "style,className,variable" {
		t.Errorf("exports = %v", r.Exports)
	}
	if st, _ := r.Exports[0][1].(map[string]any); st["fontFamily"] != "'Inter', 'Inter Fallback'" || st["fontStyle"] != "normal" {
		t.Errorf("style = %v", r.Exports[0][1])
	}
}

// TestFetchResourceShim: next/font/google's downloader runs on fetch.
func TestFetchResourceShim(t *testing.T) {
	dir := nextHello(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, r.Header.Get("User-Agent"))
	}))
	defer srv.Close()
	h := &Host{Dir: dir, Out: io.Discard}
	code, err := h.Bundle("", `
		const { fetchResource } = require(["next/dist/compiled/@next/font/dist/google/fetch-resource"][0]);
		globalThis.result = (async () => [
			(await fetchResource(`+jsonString(srv.URL+"/ok")+`, false)).toString(),
			await fetchResource(`+jsonString(srv.URL+"/missing")+`, false, "custom failure").catch((e) => e.message),
		])();
	`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "result")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := v.([]any)
	if len(got) != 2 || !strings.Contains(got[0].(string), "Chrome/104.0.0.0") || got[1] != "custom failure" {
		t.Errorf("got %v", v)
	}
}
