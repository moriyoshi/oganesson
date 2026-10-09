package next

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/moriyoshi/oganesson/runtime/app"
)

// TestPagesI18nDomains: i18n.domains routing (testdata/apps/next-i18n-domains,
// example.fr -> fr, example.nl -> nl-NL). All assertions are verified against a
// real `next start` of this fixture (Next 16.3.8).
//
//   - getStaticProps context: next build passes the GLOBAL defaultLocale to
//     every locale's page, even under domains. The index renders
//     "<locale>/<defaultLocale>": /fr is "fr/en", not "fr/fr". (og previously
//     baked the domain default; fixed by the static-prerender branch in
//     edgeSSRPagesPatches.)
//   - locale redirect for "/": detectDomainLocale maps the request host to a
//     domain, and when the Accept-Language preferred locale belongs to a
//     different domain, getLocaleRedirect returns an absolute cross-domain URL;
//     a domain serving its own default locale does not redirect.
//
// A dedicated fixture, not next-i18n, because enabling domains changes the
// per-locale getStaticProps defaultLocale that TestPagesI18n pins.
func TestPagesI18nDomains(t *testing.T) {
	dir := appDir("next-i18n-domains")
	if _, err := os.Stat(filepath.Join(dir, "node_modules/next")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/next-i18n-domains)")
	}
	out, err := Build(context.Background(), dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := packageFixture(out)
	if err != nil {
		t.Fatal(err)
	}
	h, _, err := app.Handler(fsys, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	// getStaticProps defaultLocale per locale (next start oracle: always the
	// global "en", prerendered -> cache HIT).
	ctxRe := regexp.MustCompile(`id="ctx">([^<]*)`)
	for _, c := range []struct{ path, ctx string }{
		{"/", "en/en"},
		{"/en", "en/en"},
		{"/fr", "fr/en"},
		{"/nl-NL", "nl-NL/en"},
	} {
		resp, err := http.Get(srv.URL + c.path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		got := ""
		if m := ctxRe.FindStringSubmatch(string(b)); m != nil {
			got = m[1]
		}
		if resp.StatusCode != 200 || got != c.ctx {
			t.Errorf("GET %s: %d ctx=%q, want ctx %q (next start oracle)", c.path, resp.StatusCode, got, c.ctx)
		}
	}

	// Cross-domain locale redirect for "/".
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, c := range []struct {
		host   string
		accept string
		loc    string
	}{
		{"example.fr", "nl-NL", "https://example.nl/"}, // preferred locale lives on another domain
		{"example.nl", "fr", "https://example.fr/"},    // the mirror
		{"example.fr", "fr", ""},                       // own domain + its own locale: no redirect
		{"example.nl", "nl-NL", ""},                    // own domain + its own locale: no redirect
	} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
		req.Host = c.host // the server reads req.Host, not a Host header
		req.Header.Set("Accept-Language", c.accept)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Location"); got != c.loc || (c.loc != "" && resp.StatusCode != 307) {
			t.Errorf("GET / host=%s accept=%s: %d Location %q, want %q", c.host, c.accept, resp.StatusCode, got, c.loc)
		}
	}

	// Per-domain notFound: a 404 under a domain (no locale prefix in the URL)
	// renders pages/404 with the domain's default locale; example.com is not a
	// configured domain, so it falls to the global defaultLocale. An explicit
	// locale prefix overrides the domain. Verified against the next start
	// oracle of the fixture.
	nfRe := regexp.MustCompile(`id="nf">404 \((?:<!-- -->)?([a-zA-Z-]+)`)
	for _, c := range []struct {
		host, path, locale string
	}{
		{"example.com", "/nope", "en"},
		{"example.fr", "/nope", "fr"},
		{"example.nl", "/nope", "nl-NL"},
		{"example.fr", "/nl-NL/nope", "nl-NL"}, // an explicit prefix overrides the domain
		{"example.com", "/fr/nope", "fr"},
	} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+c.path, nil)
		req.Host = c.host
		req.Header.Set("Accept", "text/html")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		got := ""
		if m := nfRe.FindStringSubmatch(string(b)); m != nil {
			got = m[1]
		}
		if resp.StatusCode != 404 || got != c.locale {
			t.Errorf("GET %s host=%s: %d locale=%q, want 404 locale %q (next start oracle)", c.path, c.host, resp.StatusCode, got, c.locale)
		}
	}
}
