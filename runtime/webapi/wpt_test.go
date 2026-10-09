package webapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/internal/gojatarget"
	"github.com/moriyoshi/oganesson/runtime/engine"
	"github.com/moriyoshi/oganesson/runtime/webapi"
)

// The WPT runner executes *.any.js files from third_party/wpt (fetched by
// scripts/sync-wpt.sh) and compares results against testdata/wpt-expectations.json,
// which lists every subtest known to fail. Run with -wpt.update to rewrite it.

var (
	wptUpdate = flag.Bool("wpt.update", false, "rewrite testdata/wpt-expectations.json from current results")
	wptFilter = flag.String("wpt.filter", "", "only run WPT files whose path contains this substring")
)

const wptRoot = "../../third_party/wpt"

var wptDirs = []string{
	"url", "encoding", "fetch/api/headers", "fetch/api/request", "fetch/api/response", "fetch/api/body",
	"fetch/data-urls", "streams", "FileAPI/blob", "FileAPI/file", "xhr/formdata", "dom/events", "dom/abort",
	"html/webappapis/structured-clone", "html/webappapis/atob", "html/webappapis/timers", "WebCryptoAPI", "compression",
}

// Files that cannot run in a server runtime at all (they need workers,
// documents, transfer of streams across realms, or take minutes in Goja).
var wptSkip = map[string]string{
	".tentative.":                                          "proposals, not shipped standards",
	"encoding/unsupported-encodings":                       "decodes via XMLHttpRequest",
	"encoding/replacement-encodings":                       "decodes via XMLHttpRequest",
	"fetch/api/request/request-cache":                      "needs the WPT server's cache.py",
	"streams/transferable":                                 "needs postMessage/workers",
	"encoding/legacy-mb-":                                  "exhaustive legacy-encoding tables: too slow",
	"encoding/single-byte-decoder":                         "needs XHR/documents",
	"crashtests":                                           "browser crash tests",
	"fetch/api/request/destination":                        "needs service workers",
	"fetch/api/response/response-cancel":                   "needs a WPT server for /fetch/api/resources/trickle.py",
	"fetch/api/response/response-stream-disturbed-by-pipe": "needs pipeTo across realms",
}

type wptResult struct {
	Harness string            `json:"harness,omitempty"` // harness-level error, if any
	Fail    map[string]string `json:"fail,omitempty"`    // subtest name -> status + message
	Pass    int               `json:"-"`
}

func TestWPT(t *testing.T) {
	if _, err := os.Stat(filepath.Join(wptRoot, "resources/testharness.js")); err != nil {
		t.Skip("third_party/wpt missing; run scripts/sync-wpt.sh")
	}
	harness, err := os.ReadFile(filepath.Join(wptRoot, "resources/testharness.js"))
	if err != nil {
		t.Fatal(err)
	}
	harnessProg := sobek.MustCompile("/resources/testharness.js", string(harness), false)
	files := collectWPT(t)

	expectPath := filepath.Join("testdata", "wpt-expectations.json")
	expected := map[string]wptResult{}
	if b, err := os.ReadFile(expectPath); err == nil {
		if err := json.Unmarshal(b, &expected); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	actual := map[string]wptResult{}
	t.Run("files", func(t *testing.T) {
		for _, f := range files {
			t.Run(f, func(t *testing.T) {
				t.Parallel()
				res := runWPTFile(t, harnessProg, f)
				mu.Lock()
				actual[f] = res
				mu.Unlock()
				if *wptUpdate {
					return
				}
				exp := expected[f]
				if res.Harness != "" && exp.Harness == "" {
					t.Errorf("harness error: %s", res.Harness)
				}
				for name, msg := range res.Fail {
					if _, ok := exp.Fail[name]; !ok {
						t.Errorf("regression: %q: %s", name, msg)
					}
				}
				for name := range exp.Fail {
					if _, ok := res.Fail[name]; !ok && res.Harness == "" {
						t.Logf("now passing (run -wpt.update): %q", name)
					}
				}
			})
		}
	})

	pass, fail := 0, 0
	byDir := map[string][2]int{}
	for f, r := range actual {
		dir := f[:strings.LastIndex(f, "/")]
		c := byDir[dir]
		c[0] += r.Pass
		c[1] += len(r.Fail)
		if r.Harness != "" {
			c[1]++
		}
		byDir[dir] = c
		pass += r.Pass
		fail += len(r.Fail)
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	var summary strings.Builder
	for _, d := range dirs {
		c := byDir[d]
		fmt.Fprintf(&summary, "  %-45s %5d pass %5d fail\n", d, c[0], c[1])
	}
	t.Logf("WPT: %d files, %d subtests pass, %d fail\n%s", len(actual), pass, fail, summary.String())

	if *wptUpdate {
		out := map[string]wptResult{}
		for f, r := range actual {
			if r.Harness != "" || len(r.Fail) > 0 {
				out[f] = r
			}
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		if err := os.WriteFile(expectPath, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func collectWPT(t *testing.T) []string {
	var files []string
	for _, d := range wptDirs {
		_ = filepath.Walk(filepath.Join(wptRoot, d), func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(p, ".any.js") {
				return nil
			}
			rel, _ := filepath.Rel(wptRoot, p)
			rel = filepath.ToSlash(rel)
			if *wptFilter != "" && !strings.Contains(rel, *wptFilter) {
				return nil
			}
			for skip := range wptSkip {
				if strings.Contains(rel, skip) {
					return nil
				}
			}
			files = append(files, rel)
			return nil
		})
	}
	sort.Strings(files)
	return files
}

type meta struct {
	scripts []string
	globals string
	long    bool
}

func readMeta(path string) meta {
	f, err := os.Open(path)
	if err != nil {
		return meta{}
	}
	defer f.Close()
	var m meta
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "// META:") {
			if line != "" && !strings.HasPrefix(line, "//") {
				break
			}
			continue
		}
		kv := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(line, "// META:")), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "script":
			m.scripts = append(m.scripts, kv[1])
		case "global":
			m.globals = kv[1]
		case "timeout":
			m.long = kv[1] == "long"
		}
	}
	return m
}

// fileTransport serves the WPT checkout for fetch() at http://wpt.test/.
type fileTransport struct{ h http.Handler }

func (f fileTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec.Result(), nil
}

const wptSetup = `
self.GLOBAL = { isWindow: () => false, isWorker: () => false, isShadowRealm: () => false };
globalThis.location = new URL(%q);
`

const wptReporter = `
add_completion_callback(function (tests, status) {
  __wptDone(JSON.stringify({
    status: status.status, message: status.message,
    tests: tests.map(function (t) { return { name: t.name, status: t.status, message: t.message }; }),
  }));
});
`

func runWPTFile(t *testing.T, harness *sobek.Program, rel string) wptResult {
	m := readMeta(filepath.Join(wptRoot, rel))
	if m.globals != "" && !strings.Contains(m.globals, "window") && !strings.Contains(m.globals, "worker") && !strings.Contains(m.globals, "shadowrealm") {
		t.Skipf("globals %q", m.globals)
	}
	timeout := 20 * time.Second
	if m.long {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	e := engine.New(engine.Options{Uncaught: func(err error) error { return nil }})
	defer e.Close()
	base := "http://wpt.test/" + rel
	_, err := webapi.Install(e, webapi.Options{
		Stdout:     io.Discard,
		Stderr:     io.Discard,
		BaseURL:    base,
		HTTPClient: &http.Client{Transport: fileTransport{http.FileServer(http.Dir(wptRoot))}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var report string
	done := false
	// Stop as soon as the harness reports: tests may leave intervals running.
	_ = e.VM.Set("__wptDone", func(s string) { report, done = s, true; cancel() })
	run := func(name, src string) error {
		_, err := e.VM.RunScript(name, src)
		return err
	}
	// Test sources go through esbuild with the Goja target, exactly like app
	// code (e.g. async generators get lowered).
	runLowered := func(name, src string) error {
		res := api.Transform(src, gojatarget.TransformOptions(api.LoaderJS, name))
		if len(res.Errors) > 0 {
			return fmt.Errorf("esbuild: %s", res.Errors[0].Text)
		}
		return run(name, string(res.Code))
	}
	harnessErr := ""
	err = e.Run(ctx, func(vm *sobek.Runtime) error {
		if err := run("setup", fmt.Sprintf(wptSetup, base)); err != nil {
			return err
		}
		if _, err := vm.RunProgram(harness); err != nil {
			return err
		}
		if err := run("reporter", wptReporter); err != nil {
			return err
		}
		for _, s := range m.scripts {
			p := s
			if !strings.HasPrefix(p, "/") {
				p = "/" + filepath.ToSlash(filepath.Join(filepath.Dir(rel), s))
			}
			src, err := os.ReadFile(filepath.Join(wptRoot, p))
			if err != nil {
				return fmt.Errorf("META script %s: %w", s, err)
			}
			if err := runLowered(p, string(src)); err != nil {
				return err
			}
		}
		src, err := os.ReadFile(filepath.Join(wptRoot, rel))
		if err != nil {
			return err
		}
		if err := runLowered("/"+rel, string(src)); err != nil {
			harnessErr = "load: " + err.Error()
		}
		// Shell environments finish once all tests are done; tell the harness
		// that no more tests are coming.
		return run("done", "done()")
	})
	if err == nil && !done {
		// The loop went idle with tests still pending: force the harness to
		// time them out so the remaining subtests report individually.
		err = e.Run(ctx, func(vm *sobek.Runtime) error { return run("timeout", "timeout()") })
	}
	if err != nil && harnessErr == "" && !done {
		harnessErr = err.Error()
	}
	if !done && harnessErr == "" {
		harnessErr = "harness did not complete"
	}

	res := wptResult{Harness: harnessErr, Fail: map[string]string{}}
	if done {
		var r struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
			Tests   []struct {
				Name    string `json:"name"`
				Status  int    `json:"status"`
				Message string `json:"message"`
			} `json:"tests"`
		}
		if err := json.Unmarshal([]byte(report), &r); err != nil {
			t.Fatal(err)
		}
		if r.Status != 0 && res.Harness == "" {
			res.Harness = fmt.Sprintf("harness status %d: %s", r.Status, r.Message)
		}
		statuses := []string{"PASS", "FAIL", "TIMEOUT", "NOTRUN", "PRECONDITION_FAILED"}
		for _, tc := range r.Tests {
			if tc.Status == 0 {
				res.Pass++
				continue
			}
			msg := tc.Message
			if len(msg) > 200 {
				msg = msg[:200] + "…"
			}
			res.Fail[tc.Name] = statuses[tc.Status] + ": " + msg
		}
	}
	if len(res.Fail) == 0 {
		res.Fail = nil
	}
	return res
}
