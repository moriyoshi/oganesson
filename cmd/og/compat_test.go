package main

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestCompatLinks: the crawler's page and asset extraction, and which
// references stay on the app.
func TestCompatLinks(t *testing.T) {
	links, assets := compatLinks([]byte(`<html><head>
<link rel="stylesheet" href="/_next/static/css/a.css"><link rel="preload" as="font" href="/f.woff2">
<link rel="icon" href="/favicon.ico"><link rel="canonical" href="/x">
<script src="/_next/static/chunks/main.js" async></script><script>inline()</script></head>
<body><a href="/about">a</a><a href="posts/1?x=1#top">b</a><a href="https://example.com/">c</a>
<img src="/_next/image?url=%2Fa.png&amp;w=640" /></body></html>`))
	wantLinks := []string{"/about", "posts/1?x=1#top", "https://example.com/"}
	wantAssets := []string{"/_next/static/css/a.css", "/f.woff2", "/favicon.ico", "/_next/static/chunks/main.js", "/_next/image?url=%2Fa.png&w=640"}
	if !reflect.DeepEqual(links, wantLinks) {
		t.Errorf("links %q, want %q", links, wantLinks)
	}
	if !reflect.DeepEqual(assets, wantAssets) {
		t.Errorf("assets %q, want %q", assets, wantAssets)
	}
	base, _ := url.Parse("http://127.0.0.1:3000")
	for ref, want := range map[string]string{
		"/about":                 "/about",
		"posts/1?x=1#top":        "/blog/posts/1?x=1",
		"https://example.com/":   "",
		"//example.com/a":        "",
		"#top":                   "",
		"mailto:a@b":             "",
		"http://127.0.0.1:3000/": "/",
	} {
		got, ok := compatLocal(base, "/blog/", ref)
		if !ok {
			got = ""
		}
		if got != want {
			t.Errorf("compatLocal(%q) = %q, want %q", ref, got, want)
		}
	}
}

// TestCompatCopy: an app is copied without node_modules or build output,
// a kept node_modules survives, "latest" Next and React are pinned, others
// are not, and a .env example is noticed.
func TestCompatCopy(t *testing.T) {
	src, dir := t.TempDir(), t.TempDir()
	for name, data := range map[string]string{
		"package.json":            `{"dependencies":{"next":"latest","react":"latest","react-dom":"^18","swr":"latest"}}`,
		"app/page.tsx":            "x",
		".env.local.example":      "KEY=",
		"node_modules/a/index.js": "src",
		".next/x":                 "x",
		"package-lock.json":       "{}",
	} {
		p := filepath.Join(src, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(data), 0o644)
	}
	_ = os.MkdirAll(filepath.Join(dir, "node_modules", "kept"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "stale.txt"), []byte("x"), 0o644)
	next, env, err := compatCopy(src, dir)
	if err != nil {
		t.Fatal(err)
	}
	if next != "latest" || !env {
		t.Errorf("next %q env %v", next, env)
	}
	pkg, _ := os.ReadFile(filepath.Join(dir, "package.json"))
	for _, want := range []string{`"next": "` + corpusNext + `"`, `"react": "` + corpusReact + `"`, `"react-dom": "^18"`, `"swr": "latest"`} {
		if !strings.Contains(string(pkg), want) {
			t.Errorf("package.json lacks %s:\n%s", want, pkg)
		}
	}
	for name, want := range map[string]bool{"package-lock.json": false, "app/page.tsx": true, "node_modules/kept": true, "node_modules/a": false, ".next": false, "stale.txt": false} {
		_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name)))
		if (err == nil) != want {
			t.Errorf("%s exists: %v, want %v", name, err == nil, want)
		}
	}
}

// TestBuiltDir: og serve serves a project's .og/, also when the project
// has a server.js of its own (a custom server, as with-http2's), and a
// build output directory itself.
func TestBuiltDir(t *testing.T) {
	project := t.TempDir()
	_ = os.WriteFile(filepath.Join(project, "server.js"), []byte("require('http2')"), 0o644)
	_ = os.MkdirAll(filepath.Join(project, ".og"), 0o755)
	_ = os.WriteFile(filepath.Join(project, ".og", "meta.json"), []byte("{}"), 0o644)
	if got, want := builtDir(project), filepath.Join(project, ".og"); got != want {
		t.Errorf("project: %s, want %s", got, want)
	}
	out := filepath.Join(project, ".og")
	if got := builtDir(out); got != out {
		t.Errorf("build output: %s, want %s", got, out)
	}
}

// TestCompatFailed: -failed reruns the apps an earlier report did not pass,
// with -run's filter on top.
func TestCompatFailed(t *testing.T) {
	report := filepath.Join(t.TempDir(), "report.json")
	_ = os.WriteFile(report, []byte(`[
  {"name":"a","stages":[{"name":"build","ok":false}]},
  {"name":"b","stages":[{"name":"build","ok":true},{"name":"crawl","ok":true}]},
  {"name":"c","stages":[{"name":"crawl","ok":false}]}
]`), 0o644)
	only, err := compatFailed(report)
	if err != nil {
		t.Fatal(err)
	}
	apps := []string{"/x/a", "/x/b", "/x/c", "/x/d"}
	if got := compatSelect(apps, nil, only); strings.Join(got, ",") != "/x/a,/x/c" {
		t.Errorf("failed: %v", got)
	}
	if got := compatSelect(apps, regexp.MustCompile("^c$"), only); strings.Join(got, ",") != "/x/c" {
		t.Errorf("failed and -run: %v", got)
	}
	if got := compatSelect(apps, nil, nil); len(got) != 4 {
		t.Errorf("all: %v", got)
	}
}

// TestCompatWorkspaces: a monorepo example (with-yarn-workspaces) has its
// Next app in a workspace: the copy pins that workspace's "latest" Next and
// React too (and drops the lockfile), and the app's stages run in it.
func TestCompatWorkspaces(t *testing.T) {
	src := t.TempDir()
	files := map[string]string{
		"package.json":                  `{"private":true,"workspaces":["packages/*"],"scripts":{"build":"yarn --cwd packages/web-app build"}}`,
		"yarn.lock":                     "# locked\n",
		"packages/foo/package.json":     `{"name":"foo","version":"1.0.0"}`,
		"packages/web-app/package.json": `{"name":"web-app","dependencies":{"next":"latest","react":"latest","react-dom":"latest","foo":"*"}}`,
	}
	for name, body := range files {
		p := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "app")
	next, _, err := compatCopy(src, dir)
	if err != nil {
		t.Fatal(err)
	}
	if next != "latest" {
		t.Errorf("next spec %q, want latest", next)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "packages", "web-app", "package.json"))
	for _, want := range []string{`"next": "` + corpusNext + `"`, `"react": "` + corpusReact + `"`, `"foo": "*"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("web-app's package.json lacks %s:\n%s", want, b)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "yarn.lock")); err == nil {
		t.Error("the lockfile was kept")
	}
	if got, want := compatAppDir(dir), filepath.Join(dir, "packages", "web-app"); got != want {
		t.Errorf("app dir %s, want %s", got, want)
	}
}

// TestCompatExpected: the summary separates failures to look into from
// those listed as expected (failing outside og too) and those of apps that
// need an external service, and names listed apps that now pass.
func TestCompatExpected(t *testing.T) {
	fail := []compatStage{{Name: "build", Error: "exit status 1"}}
	pass := []compatStage{{Name: "build", OK: true}}
	results := []*compatResult{
		{Name: "listed", Stages: fail, Expected: "fails under next build too"},
		{Name: "service", Stages: fail, NeedsEnv: true},
		{Name: "regressed", Stages: fail},
		{Name: "fixed", Stages: pass, Expected: "was broken upstream"},
		{Name: "fine", Stages: pass},
	}
	var out strings.Builder
	compatSummary(&out, results)
	for _, want := range []string{"unexpected failures: 1 (regressed)\n", "listed as expected but passing: fixed\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("summary lacks %q:\n%s", want, out.String())
		}
	}
	m, err := compatExpected(filepath.Join("..", "..", "testdata", "compat", "expected.json"))
	if err != nil || m["with-sentry"] == "" {
		t.Errorf("testdata/compat/expected.json: %v, with-sentry %q", err, m["with-sentry"])
	}
	if m, err := compatExpected(filepath.Join(t.TempDir(), "none.json")); err != nil || len(m) != 0 {
		t.Errorf("a missing file: %v %v", m, err)
	}
}

// TestCompatEnv: the -env file gives each app sorted VAR=value pairs, and
// testdata/compat/env.json has the Cache Components example's secret.
func TestCompatEnv(t *testing.T) {
	file := filepath.Join(t.TempDir(), "env.json")
	if err := os.WriteFile(file, []byte(`{"app": {"B": "2", "A": "x=y"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := compatEnv(file)
	if err != nil || strings.Join(m["app"], " ") != "A=x=y B=2" {
		t.Errorf("compatEnv: %v %v", m, err)
	}
	m, err = compatEnv(filepath.Join("..", "..", "testdata", "compat", "env.json"))
	if err != nil || len(m["with-iron-session-cache-components"]) != 1 {
		t.Errorf("testdata/compat/env.json: %v %v", m, err)
	}
	if m, err := compatEnv(filepath.Join(t.TempDir(), "none.json")); err != nil || len(m) != 0 {
		t.Errorf("a missing file: %v %v", m, err)
	}
}

// TestCompatRunEnv: compatRun gives the command the app's -env values.
func TestCompatRunEnv(t *testing.T) {
	log := filepath.Join(t.TempDir(), "run.log")
	if err := compatRun(context.Background(), time.Minute, "/bin/sh", log, []string{"OG_COMPAT_PROBE=seen"}, "-c", `echo "probe=$OG_COMPAT_PROBE"`); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "probe=seen") {
		t.Errorf("the command did not get the env value: %q", b)
	}
}
