package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"golang.org/x/net/html"
)

// og compat runs apps through og as a user would (install, build, serve)
// and crawls each served app, for the compatibility corpus (M11): Next's
// own examples (scripts/sync-next-examples.sh), or any directories of
// apps. Every stage is a child og process, so a crash is one app's
// failure. Apps are copied into a work directory first; their sources are
// never touched.

const (
	// corpusNext and corpusReact replace "latest" and "canary" in a corpus
	// app's package.json: the release the examples were taken from
	// (scripts/sync-next-examples.sh) and the React it is tested with.
	corpusNext  = "16.4.0"
	corpusReact = "19.3.0"
	// compatNextMajor is the Next major og builds.
	compatNextMajor = "16"
)

// compatPackageVersion is an installed package's version ("" if none).
func compatPackageVersion(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct{ Version string }
	_ = json.Unmarshal(b, &pkg)
	return pkg.Version
}

// compatStage is one stage's outcome.
type compatStage struct {
	Name     string        `json:"name"`
	OK       bool          `json:"ok"`
	Duration time.Duration `json:"duration"`
	Error    string        `json:"error,omitempty"`
}

// compatFetch is one crawled URL.
type compatFetch struct {
	Path   string `json:"path"`
	Status int    `json:"status"`
	Kind   string `json:"kind"` // page or asset
	From   string `json:"from,omitempty"`
}

// compatResult is one app's report.
type compatResult struct {
	Name     string        `json:"name"`
	Dir      string        `json:"dir"`
	NeedsEnv bool          `json:"needsEnv,omitempty"` // it ships a .env example, and -env has no values for it
	Env      []string      `json:"env,omitempty"`      // the names of the -env values it got
	Next     string        `json:"next,omitempty"`     // its next dependency
	Stages   []compatStage `json:"stages"`
	Fetches  []compatFetch `json:"fetches,omitempty"`
	// Expected is why the app is listed as failing outside og as well (the
	// -expected file), "" when it is not.
	Expected  string   `json:"expected,omitempty"`
	ServerLog []string `json:"serverLog,omitempty"` // error lines the server printed
}

// Outcome is the first failed stage's name, or "ok".
func (r *compatResult) Outcome() string {
	for _, s := range r.Stages {
		if !s.OK {
			return s.Name
		}
	}
	return "ok"
}

func runCompat(ctx context.Context, args []string) error {
	fset := flag.NewFlagSet("compat", flag.ExitOnError)
	work := fset.String("work", filepath.Join(".agents-workspace", "tmp", "compat"), "work directory (apps are copied here)")
	keep := fset.Bool("keep", false, "keep each app's installed packages and build output (hundreds of MB per app)")
	run := fset.String("run", "", "only apps whose names match this regular expression")
	jobs := fset.Int("j", 1, "apps to process at once")
	pages := fset.Int("pages", 20, "pages to crawl per app")
	report := fset.String("json", "", "also write the full report to this file")
	timeout := fset.Duration("timeout", 10*time.Minute, "time limit of each install and build")
	failed := fset.String("failed", "", "only the apps that did not pass in this earlier -json report")
	expectedFile := fset.String("expected", filepath.Join("testdata", "compat", "expected.json"), "apps known to fail outside og too (name -> reason); their failures are not reported as unexpected")
	envFile := fset.String("env", filepath.Join("testdata", "compat", "env.json"), "environment for apps that ship a .env example (name -> {VAR: value}; dummy values only), given to their build and server")
	_ = fset.Parse(args)
	if fset.NArg() == 0 {
		return fmt.Errorf("usage: og compat [-run re] [-j n] [-json file] dir...  (an app directory, or a directory of apps)")
	}
	var filter *regexp.Regexp
	if *run != "" {
		var err error
		if filter, err = regexp.Compile(*run); err != nil {
			return err
		}
	}
	var only map[string]bool
	if *failed != "" {
		var err error
		if only, err = compatFailed(*failed); err != nil {
			return err
		}
	}
	var apps []string
	for _, d := range fset.Args() {
		found, err := compatApps(d)
		if err != nil {
			return err
		}
		apps = append(apps, compatSelect(found, filter, only)...)
	}
	if len(apps) == 0 {
		return fmt.Errorf("compat: no apps (directories with a package.json)")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	workDir, err := filepath.Abs(*work)
	if err != nil {
		return err
	}
	expected, err := compatExpected(*expectedFile)
	if err != nil {
		return err
	}
	appEnv, err := compatEnv(*envFile)
	if err != nil {
		return err
	}
	results := make([]*compatResult, len(apps))
	sem := make(chan struct{}, max(1, *jobs))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i, a := range apps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := compatApp(ctx, self, a, workDir, *pages, *timeout, appEnv[filepath.Base(a)])
			if !*keep {
				// A corpus is hundreds of apps: their packages would fill the disk.
				_ = os.RemoveAll(filepath.Join(r.Dir, "node_modules"))
				_ = os.RemoveAll(filepath.Join(r.Dir, ".og"))
			}
			r.Expected = expected[r.Name]
			results[i] = r
			mu.Lock()
			fmt.Fprintf(os.Stderr, "compat: %-40s %s\n", r.Name, r.Outcome())
			mu.Unlock()
		}()
	}
	wg.Wait()
	compatSummary(os.Stdout, results)
	if *report != "" {
		b, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(*report, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// compatApps lists the apps in dir: dir itself when it has a package.json,
// else its subdirectories that have one.
func compatApps(dir string) ([]string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(abs, "package.json")); err == nil {
		return []string{abs}, nil
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(abs, e.Name(), "package.json")); e.IsDir() && err == nil {
			out = append(out, filepath.Join(abs, e.Name()))
		}
	}
	return out, nil
}

// compatApp runs one app through the stages: copy, install, build, serve,
// crawl. A stage that fails ends the run. env (VAR=value) goes to the
// build and the server.
func compatApp(ctx context.Context, self, src, workDir string, pages int, timeout time.Duration, env []string) *compatResult {
	name := filepath.Base(src)
	dir := filepath.Join(workDir, name)
	r := &compatResult{Name: name, Dir: dir}
	logDir := filepath.Join(workDir, "_logs", name)
	_ = os.RemoveAll(logDir)
	_ = os.MkdirAll(logDir, 0o755)
	stage := func(stageName string, f func() error) bool {
		start := time.Now()
		err := f()
		s := compatStage{Name: stageName, OK: err == nil, Duration: time.Since(start).Round(time.Millisecond)}
		if err != nil {
			s.Error = err.Error()
		}
		r.Stages = append(r.Stages, s)
		return err == nil
	}
	ok := stage("copy", func() error {
		next, needsEnv, err := compatCopy(src, dir)
		// With values for its .env, the app's failures are og's to explain.
		r.Next, r.NeedsEnv = next, needsEnv && len(env) == 0
		for _, kv := range env {
			name, _, _ := strings.Cut(kv, "=")
			r.Env = append(r.Env, name)
		}
		return err
	})
	ok = ok && stage("install", func() error {
		return compatRun(ctx, timeout, self, filepath.Join(logDir, "install.log"), nil, "install", dir)
	})
	ok = ok && stage("next-version", func() error {
		// og builds Next 16 (its templates and patches are 16's); an app
		// pinned to an older release is reported as such, not as a build
		// failure.
		v := compatPackageVersion(filepath.Join(compatAppDir(dir), "node_modules", "next"))
		if v == "" || strings.HasPrefix(v, compatNextMajor+".") {
			return nil
		}
		return fmt.Errorf("next %s is installed; og builds Next %s.x", v, compatNextMajor)
	})
	ok = ok && stage("build", func() error {
		return compatRun(ctx, timeout, self, filepath.Join(logDir, "build.log"), env, "build", compatAppDir(dir))
	})
	if !ok {
		return r
	}
	var addr string
	var srv *exec.Cmd
	var srvLog *os.File
	ok = stage("serve", func() error {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		addr = l.Addr().String()
		l.Close()
		if srvLog, err = os.Create(filepath.Join(logDir, "serve.log")); err != nil {
			return err
		}
		app := compatAppDir(dir)
		srv = exec.CommandContext(ctx, self, "serve", "-addr", addr, app)
		srv.Dir = app
		srv.Env = append(os.Environ(), env...)
		srv.Stdout, srv.Stderr = srvLog, srvLog
		if err := srv.Start(); err != nil {
			return err
		}
		for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			if c, err := net.Dial("tcp", addr); err == nil {
				c.Close()
				return nil
			}
			if srv.ProcessState != nil {
				break
			}
		}
		return fmt.Errorf("the server did not start listening:\n%s", compatTail(filepath.Join(logDir, "serve.log")))
	})
	if srv != nil && srv.Process != nil {
		defer func() {
			_ = srv.Process.Kill()
			_ = srv.Wait()
			srvLog.Close()
		}()
	}
	if !ok {
		return r
	}
	stage("crawl", func() error {
		r.Fetches = compatCrawl(ctx, "http://"+addr, pages)
		time.Sleep(200 * time.Millisecond) // let the server's log catch up
		r.ServerLog = compatErrorLines(filepath.Join(logDir, "serve.log"))
		var bad []string
		for _, f := range r.Fetches {
			if f.Status >= 500 || f.Status == 0 || f.Kind == "asset" && f.Status >= 400 {
				bad = append(bad, fmt.Sprintf("%s %s %d", f.Kind, f.Path, f.Status))
			}
		}
		if len(bad) > 0 {
			return errors.New(strings.Join(bad, "\n"))
		}
		return nil
	})
	return r
}

// compatCopy copies an app into dir (leaving dir's node_modules, kept with
// -keep, for the next install) and pins "latest" / "canary" Next and React versions. It
// returns the app's next dependency and whether it ships a .env example
// (the app needs settings, often an external service).
func compatCopy(src, dir string) (next string, needsEnv bool, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}
	// Clear what an earlier run left, but the installed packages.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "node_modules" {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return "", false, err
			}
		}
	}
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".next", ".og", ".git":
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dir, rel), 0o755)
		}
		if strings.HasPrefix(d.Name(), ".env") && strings.Contains(d.Name(), "example") {
			needsEnv = true
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, rel), b, 0o644)
	})
	if err != nil {
		return "", needsEnv, err
	}
	// The root's package.json and its workspaces' (a monorepo's app is a
	// workspace).
	pinned := false
	for _, pkgDir := range append([]string{dir}, compatWorkspaces(dir)...) {
		v, p, err := compatPin(filepath.Join(pkgDir, "package.json"))
		if err != nil {
			return "", needsEnv, err
		}
		if next == "" {
			next = v
		}
		pinned = pinned || p
	}
	if pinned {
		// A lockfile would install the versions it locked, not the pins
		// (npm would update it for the changed package.json).
		for _, lock := range []string{"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb"} {
			_ = os.Remove(filepath.Join(dir, lock))
		}
	}
	return next, needsEnv, nil
}

// compatPin replaces "latest", "canary" and "rc" Next and React versions in
// a package.json with the corpus pins, and reports its next version.
func compatPin(pkgPath string) (next string, pinned bool, err error) {
	b, err := os.ReadFile(pkgPath)
	if err != nil {
		return "", false, err
	}
	var pkg map[string]any
	if err := json.Unmarshal(b, &pkg); err != nil {
		return "", false, err
	}
	pins := map[string]string{"next": corpusNext, "react": corpusReact, "react-dom": corpusReact}
	for _, field := range []string{"dependencies", "devDependencies"} {
		deps, _ := pkg[field].(map[string]any)
		for dep, v := range deps {
			s, _ := v.(string)
			if dep == "next" && next == "" {
				next = s
			}
			if pin, ok := pins[dep]; ok && (s == "latest" || s == "canary" || s == "rc") {
				deps[dep] = pin
				pinned = true
			}
		}
	}
	out, _ := json.MarshalIndent(pkg, "", "  ")
	return next, pinned, os.WriteFile(pkgPath, append(out, '\n'), 0o644)
}

// compatWorkspaces lists the directories of a root package.json's
// workspaces (globs, or { packages }).
func compatWorkspaces(dir string) []string {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if json.Unmarshal(b, &pkg) != nil || len(pkg.Workspaces) == 0 {
		return nil
	}
	var globs []string
	if json.Unmarshal(pkg.Workspaces, &globs) != nil {
		var obj struct {
			Packages []string `json:"packages"`
		}
		_ = json.Unmarshal(pkg.Workspaces, &obj)
		globs = obj.Packages
	}
	var out []string
	for _, g := range globs {
		matches, _ := filepath.Glob(filepath.Join(dir, filepath.FromSlash(strings.TrimSuffix(g, "/"))))
		sort.Strings(matches)
		for _, m := range matches {
			if _, err := os.Stat(filepath.Join(m, "package.json")); err == nil {
				out = append(out, m)
			}
		}
	}
	return out
}

// compatAppDir is the directory og builds: the app itself, or in a
// monorepo the first workspace that depends on next.
func compatAppDir(dir string) string {
	if compatDependsOnNext(dir) {
		return dir
	}
	for _, ws := range compatWorkspaces(dir) {
		if compatDependsOnNext(ws) {
			return ws
		}
	}
	return dir
}

func compatDependsOnNext(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Dependencies, DevDependencies map[string]string
	}
	_ = json.Unmarshal(b, &pkg)
	_, a := pkg.Dependencies["next"]
	_, d := pkg.DevDependencies["next"]
	return a || d
}

// compatRun runs og with args, its output into logFile; an error carries
// the log's error lines (or its tail).
func compatRun(ctx context.Context, timeout time.Duration, self, logFile string, env []string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	f, err := os.Create(logFile)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, self, args...)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.Env = append(os.Environ(), env...)
	err = cmd.Run()
	f.Close()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		err = fmt.Errorf("timed out after %s", timeout)
	}
	return fmt.Errorf("%w\n%s", err, compatTail(logFile))
}

// compatTail is the end of a log, at most 15 lines.
func compatTail(file string) string {
	b, _ := os.ReadFile(file)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > 15 {
		lines = lines[len(lines)-15:]
	}
	return strings.Join(lines, "\n")
}

var compatErrorRe = regexp.MustCompile(`(?i)\b(error|exception|panic|unhandled)\b`)

// compatErrorLines are a server log's lines that report errors (stack
// frames left out), at most 20.
func compatErrorLines(file string) []string {
	b, _ := os.ReadFile(file)
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "at ") || !compatErrorRe.MatchString(t) {
			continue
		}
		out = append(out, t)
		if len(out) == 20 {
			break
		}
	}
	return out
}

// compatCrawl fetches base's pages breadth first from /, following
// same-origin links (up to maxPages pages), and the scripts, stylesheets
// and images they reference (each once).
func compatCrawl(ctx context.Context, base string, maxPages int) []compatFetch {
	client := &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Host != via[0].URL.Host {
				return http.ErrUseLastResponse // leaves the app
			}
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
	baseURL, _ := url.Parse(base)
	var out []compatFetch
	seen := map[string]bool{"/": true}
	queue := []string{"/"}
	for len(queue) > 0 && maxPages > 0 {
		p := queue[0]
		queue = queue[1:]
		maxPages--
		status, body, ctype := compatGet(ctx, client, base+p)
		out = append(out, compatFetch{Path: p, Status: status, Kind: "page"})
		if !strings.Contains(ctype, "text/html") {
			continue
		}
		links, assets := compatLinks(body)
		for _, a := range assets {
			ap, ok := compatLocal(baseURL, p, a)
			if !ok || seen[ap] {
				continue
			}
			seen[ap] = true
			st, _, _ := compatGet(ctx, client, base+ap)
			out = append(out, compatFetch{Path: ap, Status: st, Kind: "asset", From: p})
		}
		for _, l := range links {
			lp, ok := compatLocal(baseURL, p, l)
			if !ok || seen[lp] || strings.HasPrefix(lp, "/api/") {
				continue
			}
			if ext := path.Ext(strings.SplitN(lp, "?", 2)[0]); ext != "" && ext != ".html" {
				continue
			}
			seen[lp] = true
			queue = append(queue, lp)
		}
	}
	return out
}

func compatGet(ctx context.Context, client *http.Client, u string) (int, []byte, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, ""
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, ""
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	return res.StatusCode, body, res.Header.Get("Content-Type")
}

// compatLinks extracts a page's links (a href) and assets (script src,
// link href of stylesheets / preloads / icons, img src).
func compatLinks(body []byte) (links, assets []string) {
	z := html.NewTokenizer(bytes.NewReader(body))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		t := z.Token()
		attr := func(k string) string {
			for _, a := range t.Attr {
				if a.Key == k {
					return a.Val
				}
			}
			return ""
		}
		switch t.Data {
		case "a":
			if h := attr("href"); h != "" {
				links = append(links, h)
			}
		case "script", "img":
			if s := attr("src"); s != "" {
				assets = append(assets, s)
			}
		case "link":
			switch rel := attr("rel"); {
			case strings.Contains(rel, "stylesheet"), strings.Contains(rel, "preload"), strings.Contains(rel, "icon"), rel == "manifest":
				if h := attr("href"); h != "" {
					assets = append(assets, h)
				}
			}
		}
	}
}

// compatLocal resolves ref against page; ok when it stays on the app.
func compatLocal(base *url.URL, page, ref string) (string, bool) {
	if strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "mailto:") || strings.HasPrefix(ref, "javascript:") || strings.HasPrefix(ref, "#") {
		return "", false
	}
	pu, err := base.Parse(page)
	if err != nil {
		return "", false
	}
	u, err := pu.Parse(ref)
	if err != nil || u.Host != base.Host || u.Scheme != base.Scheme {
		return "", false
	}
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p, true
}

// compatSummary prints a line per app and the counts per outcome.
func compatSummary(w io.Writer, results []*compatResult) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "APP\tOUTCOME\tPAGES\tASSETS\tENV\tNEXT\tFIRST ERROR")
	counts := map[string]int{}
	for _, r := range results {
		o := r.Outcome()
		counts[o]++
		var pages, assets, okPages, okAssets int
		for _, f := range r.Fetches {
			if f.Kind == "page" {
				pages++
				if f.Status < 400 && f.Status != 0 {
					okPages++
				}
			} else {
				assets++
				if f.Status < 400 && f.Status != 0 {
					okAssets++
				}
			}
		}
		first := ""
		for _, s := range r.Stages {
			if !s.OK {
				first = compatFirstError(s.Error)
				break
			}
		}
		if first == "" && len(r.ServerLog) > 0 {
			first = "(log) " + r.ServerLog[0]
		}
		env := ""
		switch {
		case r.NeedsEnv:
			env = "env"
		case len(r.Env) > 0:
			env = "env set"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d/%d\t%d/%d\t%s\t%s\t%s\n", r.Name, o, okPages, pages, okAssets, assets, env, r.Next, first)
	}
	tw.Flush()
	var keys []string
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprint(w, "\n")
	for _, k := range keys {
		fmt.Fprintf(w, "%s: %d  ", k, counts[k])
	}
	fmt.Fprintf(w, "(of %d)\n", len(results))
	// Failures neither listed as expected nor in an app that needs an
	// external service (a .env example) are the ones to look into; a listed
	// app that passes can leave the list.
	var unexpected, passing []string
	for _, r := range results {
		switch ok := r.Outcome() == "ok"; {
		case !ok && r.Expected == "" && !r.NeedsEnv:
			unexpected = append(unexpected, r.Name)
		case ok && r.Expected != "":
			passing = append(passing, r.Name)
		}
	}
	fmt.Fprintf(w, "unexpected failures: %d", len(unexpected))
	if len(unexpected) > 0 {
		fmt.Fprintf(w, " (%s)", strings.Join(unexpected, ", "))
	}
	fmt.Fprintln(w)
	if len(passing) > 0 {
		fmt.Fprintf(w, "listed as expected but passing: %s\n", strings.Join(passing, ", "))
	}
}

// compatExpected reads the expected-failures file (name -> reason); a
// missing file lists none.
func compatExpected(file string) (map[string]string, error) {
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("compat: %s: %w", file, err)
	}
	return m, nil
}

// compatEnv reads the -env file (name -> {VAR: value}) as each app's
// VAR=value list, sorted; a missing file gives none.
func compatEnv(file string) (map[string][]string, error) {
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string][]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("compat: %s: %w", file, err)
	}
	out := map[string][]string{}
	for name, vars := range m {
		for k, v := range vars {
			out[name] = append(out[name], k+"="+v)
		}
		sort.Strings(out[name])
	}
	return out, nil
}

// compatFirstError picks a stage error's most telling line, shortened.
func compatFirstError(s string) string {
	lines := strings.Split(s, "\n")
	pick := lines[0]
	for _, l := range lines[1:] {
		if compatErrorRe.MatchString(l) && !strings.HasPrefix(strings.TrimSpace(l), "at ") {
			pick = strings.TrimSpace(l)
			break
		}
	}
	if len(pick) > 160 {
		pick = pick[:160] + "…"
	}
	return pick
}

// compatFailed reads an earlier -json report: the names of the apps that
// did not pass.
func compatFailed(file string) (map[string]bool, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var prev []*compatResult
	if err := json.Unmarshal(b, &prev); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	only := map[string]bool{}
	for _, r := range prev {
		if r.Outcome() != "ok" {
			only[r.Name] = true
		}
	}
	return only, nil
}

// compatSelect keeps the apps whose names match filter (when set) and are
// in only (when set).
func compatSelect(apps []string, filter *regexp.Regexp, only map[string]bool) []string {
	var out []string
	for _, a := range apps {
		name := filepath.Base(a)
		if (filter == nil || filter.MatchString(name)) && (only == nil || only[name]) {
			out = append(out, a)
		}
	}
	return out
}
