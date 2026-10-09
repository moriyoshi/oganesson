package next

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/moriyoshi/oganesson/runtime/app"
)

var (
	i18nOnce sync.Once
	i18nOut  *Output
	i18nFS   fstest.MapFS
	i18nErr  error
)

// TestPagesI18n: next.config i18n (Pages Router). Locale prefixes route to
// the pages with the locale in getStaticProps / getServerSideProps and
// __NEXT_DATA__; the default locale is unprefixed or prefixed; each
// locale's static pages and getStaticPaths paths are prerendered, HTML and
// locale-prefixed data together; "/" redirects by NEXT_LOCALE and
// Accept-Language ahead of the cache; API routes are not localized.
func TestPagesI18n(t *testing.T) {
	dir := appDir("next-i18n")
	if _, err := os.Stat(filepath.Join(dir, "node_modules/next")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/next-i18n)")
	}
	i18nOnce.Do(func() {
		i18nOut, i18nErr = Build(context.Background(), dir, Options{})
		if i18nErr == nil {
			i18nFS, i18nErr = packageFixture(i18nOut)
		}
	})
	if i18nErr != nil {
		t.Fatal(i18nErr)
	}
	h, _, err := app.Handler(i18nFS, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(p string, hdr map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+p, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	data := "/_next/data/" + i18nOut.BuildID
	for _, c := range []struct {
		path   string
		status int
		cache  string
		want   []string
	}{
		// Data first: what the build's prerender stored (locale-prefixed data
		// URLs, as the client requests them), before any render below.
		{data + "/en.json", 200, "HIT", []string{`"greeting":"Hello"`}},
		{data + "/fr.json", 200, "HIT", []string{`"greeting":"Bonjour"`}},
		{data + "/fr/posts/1.json", 200, "HIT", []string{`"locale":"fr"`}},
		{data + "/en/posts/1.json", 200, "HIT", []string{`"locale":"en"`}},
		{"/", 200, "HIT", []string{`id="greeting">Hello`, `en/en/3`, `"locale":"en"`, `"locales":["en","fr","nl-NL"]`, `"defaultLocale":"en"`}},
		{"/fr", 200, "HIT", []string{`id="greeting">Bonjour`, `fr/en/3`, `"locale":"fr"`}},
		{"/nl-NL", 200, "HIT", []string{`id="greeting">Hallo`}},
		{"/en", 200, "", []string{`id="greeting">Hello`}},
		{"/fr/about", 200, "", []string{`about in <!-- -->fr`}},
		{"/about", 200, "", []string{`about in <!-- -->en`}},
		{"/posts/1", 200, "HIT", []string{`post <!-- -->1<!-- --> (<!-- -->en<!-- -->)`}},
		{"/fr/posts/1", 200, "HIT", []string{`post <!-- -->1<!-- --> (<!-- -->fr<!-- -->)`}},
		{"/en/posts/1", 200, "", []string{`(<!-- -->en<!-- -->)`}},
		{"/posts/2", 404, "", []string{`id="nf">404 (<!-- -->en`}},
		{"/fr/nope", 404, "", []string{`id="nf">404 (<!-- -->fr`}},
		{"/de", 404, "", nil},
		{data + "/fr/about.json", 200, "", []string{`{"pageProps":{"locale":"fr"},"__N_SSP":true}`}},
		{data + "/fr/posts/2.json", 404, "", nil},
	} {
		resp, body := get(c.path, nil)
		if resp.StatusCode != c.status {
			t.Errorf("GET %s: %d, want %d\n%.300s", c.path, resp.StatusCode, c.status, body)
			continue
		}
		if c.cache != "" && resp.Header.Get("x-nextjs-cache") != c.cache {
			t.Errorf("GET %s: x-nextjs-cache %q, want %q", c.path, resp.Header.Get("x-nextjs-cache"), c.cache)
		}
		for _, w := range c.want {
			if !strings.Contains(body, w) {
				t.Errorf("GET %s lacks %s\n%.500s", c.path, w, body)
			}
		}
	}
	// Locale detection for "/" (router-server's getLocaleRedirect), ahead of
	// the cached "/".
	for _, c := range []struct {
		path string
		hdr  map[string]string
		loc  string
	}{
		{"/", map[string]string{"Accept-Language": "fr"}, "/fr"},
		{"/", map[string]string{"Accept-Language": "nl-NL,nl;q=0.9"}, "/nl-NL"},
		{"/?x=1", map[string]string{"Accept-Language": "fr"}, "/fr?x=1"},
		{"/", map[string]string{"Cookie": "NEXT_LOCALE=fr", "Accept-Language": "nl-NL"}, "/fr"},
		{"/", map[string]string{"Accept-Language": "de"}, ""},
		{"/", map[string]string{"Accept-Language": "en"}, ""},
		{"/about", map[string]string{"Accept-Language": "fr"}, ""},
		{"/nl-NL", map[string]string{"Accept-Language": "fr"}, ""},
	} {
		resp, _ := get(c.path, c.hdr)
		if got := resp.Header.Get("Location"); got != c.loc || (c.loc != "" && resp.StatusCode != 307) {
			t.Errorf("GET %s %v: %d Location %q, want %q", c.path, c.hdr, resp.StatusCode, got, c.loc)
		}
	}
}
