package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing/fstest"
	"time"

	"github.com/moriyoshi/oganesson/internal/frameworks/next"
	"github.com/moriyoshi/oganesson/internal/frameworks/vite"
	"github.com/moriyoshi/oganesson/internal/jshost"
	"github.com/moriyoshi/oganesson/internal/nativegen"
	"github.com/moriyoshi/oganesson/pack"
	"github.com/moriyoshi/oganesson/runtime/app"
	"github.com/moriyoshi/oganesson/runtime/notices"
)

func runBuild(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	out := fs.String("o", "", "also write a single self-contained executable to this path")
	target := fs.String("target", "", "platform of the -o executable, as goos/goarch (default: this one)")
	rt := fs.String("runtime", "", "og executable built for -target to package the app with (default: the runtime cache)")
	ejectDir := fs.String("eject", "", "also write the app as a standalone Go module to this directory")
	ogSource := fs.String("og-source", "", "an Oganesson checkout to build against, needed only for an unpublished og: the -eject module's replace directives, the slim -o runtime, and the -o executable of a project with native Go modules (default: $OG_SOURCE)")
	goExe := fs.String("go", "", "the Go toolchain that compiles the -o runtime, a path or a name on PATH (default: $OG_GO, else \"go\")")
	readable := fs.Bool("readable", false, "leave browser bundles and stylesheets unminified")
	_ = fs.Parse(args)
	if *ogSource != "" && *ejectDir == "" && *out == "" {
		return fmt.Errorf("-og-source needs -eject or -o")
	}
	if *out == "" && (*target != "" || *rt != "") {
		return fmt.Errorf("-target and -runtime need -o")
	}
	dir, err := filepath.Abs(projectDir(fs))
	if err != nil {
		return err
	}
	natives, err := nativegen.Scan(dir)
	if err != nil {
		return err
	}
	// Resolve the runtime first: a missing one should not cost a build.
	var runtimeExe, prerenderExe string
	if *out != "" {
		gotool := goTool(*goExe)
		goos, goarch, err := parseTarget(*target)
		if err != nil {
			return err
		}
		if goos == "windows" && !strings.HasSuffix(*out, ".exe") {
			*out += ".exe"
		}
		if natives == nil {
			if *rt != "" {
				// An explicit -runtime: package the og the user named as-is.
				if runtimeExe, err = selectRuntime(*target, *rt); err != nil {
					return err
				}
			} else if source, serr := ogSourceDir(*ogSource); serr != nil {
				return serr
			} else if reason := slimRuntimeReason(source, gotool); reason != "" {
				// A slim serve-only runtime needs the Go toolchain; without it,
				// copy the whole og so the build still works with no toolchain.
				fmt.Fprintf(os.Stderr, "note: %s; packaging the full og runtime for %s/%s (install Go for a smaller app)\n", reason, goos, goarch)
				if runtimeExe, err = selectRuntime(*target, ""); err != nil {
					return err
				}
			} else {
				tmp, err := os.MkdirTemp("", "og-runtime-")
				if err != nil {
					return err
				}
				defer os.RemoveAll(tmp)
				runtimeExe = filepath.Join(tmp, "app")
				fmt.Fprintf(os.Stderr, "building a slim runtime for %s/%s\n", goos, goarch)
				if err := buildSlimRuntime(ctx, gotool, source, goos, goarch, runtimeExe); err != nil {
					return err
				}
			}
		} else {
			if *rt != "" {
				return fmt.Errorf("-runtime cannot package a project with native Go modules (%s/): og builds its executable", nativegen.Dir)
			}
			source, err := ogSourceDir(*ogSource)
			if err != nil {
				return err
			}
			tmp, err := os.MkdirTemp("", "og-runtime-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(tmp)
			runtimeExe = filepath.Join(tmp, "app")
			fmt.Fprintf(os.Stderr, "building the app's Go code (%d native module(s)) for %s/%s\n", len(natives.Modules), goos, goarch)
			if err := buildNativeRuntime(ctx, gotool, natives, source, goos, goarch, runtimeExe); err != nil {
				return err
			}
			if goos == runtime.GOOS && goarch == runtime.GOARCH {
				prerenderExe = runtimeExe
			} else {
				// Cross build: the target binary cannot run here, so build a
				// host binary too, only to prerender with the project's Go
				// functions (otherwise static pages, and a generateStaticParams
				// that calls them, render without the native modules). The Go
				// toolchain and -og-source are already required on this path.
				prerenderExe = filepath.Join(tmp, "prerender")
				fmt.Fprintf(os.Stderr, "building the app's Go code for %s/%s to prerender static pages\n", runtime.GOOS, runtime.GOARCH)
				if err := buildNativeRuntime(ctx, gotool, natives, source, runtime.GOOS, runtime.GOARCH, prerenderExe); err != nil {
					return err
				}
			}
		}
	}
	if natives != nil && prerenderExe == "" {
		fmt.Fprintf(os.Stderr, "note: static pages are prerendered without the native Go modules (%s/); those calling them render on request\n", nativegen.Dir)
	}
	if err := writeTypes(dir); err != nil {
		return err
	}
	start := time.Now()
	var files map[string][]byte
	if !isNextProject(dir) && vite.Detect(dir) {
		if files, err = buildVite(ctx, dir, start); err != nil {
			return err
		}
	} else if files, err = buildNext(ctx, dir, start, *readable, prerenderExe); err != nil {
		return err
	}

	if *ejectDir != "" {
		if err := eject(*ejectDir, dir, files, *ogSource, *readable, natives); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "ejected the app as a Go module into %s\n", *ejectDir)
	}
	if *out != "" {
		var buf bytes.Buffer
		if err := pack.Write(&buf, runtimeExe, files); err != nil {
			return err
		}
		if err := writeExecutable(*out, buf.Bytes()); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s (%.1f MB)\n", *out, float64(buf.Len())/(1<<20))
	}
	return nil
}

// writeExecutable writes an app binary as a new file (written beside the
// path, then renamed over it), never in place: macOS kills a signed binary
// whose file changed under its cached signature, Linux refuses to write a
// running one, and a failed write leaves the old binary whole.
func writeExecutable(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o755)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

func mapFS(files map[string][]byte) fstest.MapFS {
	m := fstest.MapFS{}
	for name, b := range files {
		m[name] = &fstest.MapFile{Data: b}
	}
	return m
}

// matchesPattern reports whether path instantiates pattern (same depth,
// static segments equal); good enough for the build summary.
func matchesPattern(pattern, path string) bool {
	ps, xs := strings.Split(pattern, "/"), strings.Split(path, "/")
	if len(ps) != len(xs) && !strings.Contains(pattern, "...") {
		return false
	}
	for i := range ps {
		if i >= len(xs) {
			return false
		}
		if !strings.HasPrefix(ps[i], "[") && ps[i] != xs[i] {
			return false
		}
	}
	return true
}

// delocalize strips a locale prefix ("/fr/posts/1" -> "/posts/1").
func delocalize(p string, locales []string) string {
	for _, l := range locales {
		if p == "/"+l {
			return "/"
		}
		if strings.HasPrefix(p, "/"+l+"/") {
			return p[len(l)+1:]
		}
	}
	return p
}

// buildNext builds a Next.js project into the app payload (and .og/).
// With prerenderExe (an app binary with the project's native Go modules,
// for this platform), static pages prerender in it.
func buildNext(ctx context.Context, dir string, start time.Time, readable bool, prerenderExe string) (map[string][]byte, error) {
	res, err := next.Build(ctx, dir, next.Options{Log: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }, Readable: readable})
	if err != nil {
		return nil, err
	}
	meta, err := res.Meta()
	if err != nil {
		return nil, err
	}
	metaJSON, _ := json.MarshalIndent(meta, "", "  ")
	files := map[string][]byte{app.ServerFile: []byte(res.ServerJS), app.MetaFile: metaJSON}
	for name, b := range res.Static {
		files[app.NextDir+"/"+name] = b
	}
	for name, b := range res.Public {
		files[app.PublicDir+"/"+name] = b
	}
	for name, b := range res.Files {
		files[next.FilesDir+"/"+name] = b
	}
	if len(res.Notices) > 0 {
		files[notices.AppFile] = res.Notices
	}

	// Prerender static pages through the freshly built server and bundle the
	// results, like next build's static generation.
	var pre *app.PrerenderResult
	if prerenderExe != "" {
		pre, err = app.PrerenderIn(prerenderExe, files, res.PrerenderRoutes())
	} else {
		pre, err = app.Prerender(mapFS(files), res.PrerenderRoutes())
	}
	if err != nil {
		return nil, fmt.Errorf("prerender: %w", err)
	}
	files[app.PrerenderFile] = pre.JSON

	if err := writeOutDir(dir, files); err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "built %d route(s) into %s in %s\n", len(res.Routes), filepath.Join(dir, ".og"), time.Since(start).Round(time.Millisecond))
	static := map[string]bool{}
	for _, p := range pre.Static {
		static[p] = true
	}
	partial := map[string]bool{}
	for _, p := range pre.Partial {
		partial[p] = true
	}
	routePaths := map[string]bool{}
	for _, r := range res.Routes {
		routePaths[r.Pathname] = true
	}
	for _, r := range res.Routes {
		kind := "ƒ dynamic"
		switch {
		case partial[r.Pathname]:
			kind = "◐ partial prerender (static shell, dynamic parts per request)"
		case static[r.Pathname]:
			kind = "○ static (prerendered)"
		case r.Pages && r.IsRoute:
			kind = "ƒ API route"
		case r.IsRoute:
			kind = "ƒ route handler"
		case strings.Contains(r.Pathname, "["):
			kind = "● dynamic segment (other params cached on first request if static)"
		}
		fmt.Fprintf(os.Stderr, "  %-22s %s\n", r.Pathname, kind)
		for _, l := range res.Locales {
			if lp := "/" + l + strings.TrimSuffix(r.Pathname, "/"); l != res.DefaultLocale && static[lp] {
				fmt.Fprintf(os.Stderr, "    ├ %s\n", lp)
			}
		}
		if strings.Contains(r.Pathname, "[") {
			for _, p := range pre.Static {
				if !routePaths[p] && matchesPattern(r.Pathname, delocalize(p, res.Locales)) {
					fmt.Fprintf(os.Stderr, "    ├ %s\n", p)
				}
			}
		}
	}

	return files, nil
}

// buildVite builds a Vite project: Vite's production build (the client,
// served like vite preview).
func buildVite(ctx context.Context, dir string, start time.Time) (map[string][]byte, error) {
	res, err := vite.Build(ctx, dir, vite.Options{Log: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }})
	if err != nil {
		return nil, err
	}
	files := res.Payload()
	if err := writeOutDir(dir, files); err != nil {
		return nil, err
	}
	kind := res.AppType
	count := fmt.Sprintf("%d client file(s)", len(res.Client))
	switch {
	case res.Files != nil:
		kind += ", with a server program"
		count = fmt.Sprintf("%d project file(s)", len(res.Files))
	case res.ServerJS != "":
		kind += ", with a server"
	}
	fmt.Fprintf(os.Stderr, "built the Vite app (%s, %s) into %s in %s\n", kind, count, filepath.Join(dir, ".og"), time.Since(start).Round(time.Millisecond))
	return files, nil
}

// writeOutDir replaces .og/ with the payload files.
func writeOutDir(dir string, files map[string][]byte) error {
	outDir := filepath.Join(dir, ".og")
	if err := os.RemoveAll(outDir); err != nil {
		return err
	}
	for name, b := range files {
		p := filepath.Join(outDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// isNextProject reports a Next.js project: a next.config.* or a "next"
// dependency.
func isNextProject(dir string) bool {
	if jshost.FindNextConfig(dir) != "" {
		return true
	}
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct{ Dependencies, DevDependencies map[string]string }
	_ = json.Unmarshal(b, &pkg)
	_, dep := pkg.Dependencies["next"]
	_, dev := pkg.DevDependencies["next"]
	return dep || dev
}
