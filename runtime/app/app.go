// Package app loads a built Oganesson application (server bundle plus metadata)
// from any fs.FS, such as a .og directory or an executable's payload.
package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/moriyoshi/oganesson/runtime/native"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"

	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/internal/wasm"
	"github.com/moriyoshi/oganesson/runtime/engine"
	"github.com/moriyoshi/oganesson/runtime/imageopt"
	"github.com/moriyoshi/oganesson/runtime/isr"
	"github.com/moriyoshi/oganesson/runtime/kv"
	"github.com/moriyoshi/oganesson/runtime/natives"
	"github.com/moriyoshi/oganesson/runtime/nodeshim"
	"github.com/moriyoshi/oganesson/runtime/server"
	"github.com/moriyoshi/oganesson/runtime/vfs"
	"github.com/moriyoshi/oganesson/runtime/webapi"
)

// File names inside a built application.
const (
	ServerFile = "server.js"
	MetaFile   = "meta.json"
)

// FormatVersion is the version of the built-app format (server.js's
// contract with the runtime, meta.json, prerender.json). A runtime serves
// only apps built for its own format: an app binary may be packaged with an
// og runtime for another platform (og build -target), which must come from
// the same source as the og that built the app.
const FormatVersion = 6 // 6: meta.trailingSlash (canonical prerender + redirect); 4: Vite apps (framework "vite", client/); 3: meta.json cacheBypass (draft mode); 2: og.cache.staticPaths

// Meta describes a built application.
type Meta struct {
	// Format is the FormatVersion the app was built for.
	Format    int               `json:"format"`
	Framework string            `json:"framework"`
	BuildID   string            `json:"buildId"`
	Env       map[string]string `json:"env"`
	Routes    []string          `json:"routes"`
	// HasMiddleware: the app's proxy runs in front of the page cache.
	HasMiddleware bool `json:"hasMiddleware,omitempty"`
	// Images is next.config's images; when set, Oganesson serves the image
	// optimizer at images.path (/_next/image).
	Images   *imageopt.Config `json:"images,omitempty"`
	BasePath string           `json:"basePath,omitempty"`
	// TrailingSlash: pages are prerendered and served under the slash form
	// (next.config's trailingSlash), so dynamic paths prerender canonically.
	TrailingSlash bool `json:"trailingSlash,omitempty"`
	// AppType is Vite's appType ("spa", "mpa", "custom"): how a Vite
	// client build serves unknown paths (ClientDir).
	AppType string `json:"appType,omitempty"`
	// Files is the payload directory of the project files the server reads
	// (a server program's outputs, a Next app's traced files), mounted at
	// "/" (the runtime's cwd) under an in-memory writable layer
	// (runtime/vfs's Overlay).
	Files string `json:"files,omitempty"`
	// ClientDir is the payload directory of a Vite app's client build
	// (default: ClientDir).
	ClientDir string `json:"clientDir,omitempty"`
	// CacheBypass: requests carrying this cookie with the value of this
	// environment variable bypass the page cache (Next's draft mode).
	CacheBypass *CacheBypass `json:"cacheBypass,omitempty"`
	// PrismaMigrate, when set, runs `prisma migrate deploy` once at startup.
	PrismaMigrate *PrismaMigrate `json:"prismaMigrate,omitempty"`

	// Close releases the process-wide resources this handler created — the
	// native service pools that outlive request engines (pg's connection
	// pools). A long-running server never needs it (the pools stay warm for
	// the process's life); a caller that tears the handler down, such as a
	// test, must call it to release held connections. nil when there are
	// none. Not serialized.
	Close func() `json:"-"`
}

// CacheBypass names a cookie whose valid value (from the environment)
// sends a request around the page cache.
type CacheBypass struct {
	Cookie   string `json:"cookie"`
	ValueEnv string `json:"valueEnv"`
}

// PrismaMigrate describes a Prisma project's `migrate deploy`, applied once at
// app startup: the packaged migrations are run against the datasource before
// the server accepts requests. Set by the build when a project has
// prisma/migrations; nil otherwise.
type PrismaMigrate struct {
	// Dir is the packaged migrations directory (in Meta.Files), e.g.
	// "prisma/migrations"; its subdirectories each hold a migration.sql.
	Dir string `json:"dir"`
	// Provider is the datasource provider from the .prisma schema
	// ("sqlite", "postgresql", …): it selects the driver adapter.
	Provider string `json:"provider"`
	// URL is the datasource connection string when the project's
	// prisma.config.ts gives one statically (sqlite). Empty for the adapter
	// case, where URLEnv names the environment variable holding it instead.
	URL    string `json:"url,omitempty"`
	URLEnv string `json:"urlEnv,omitempty"`
	// Program is the bundled migrator program (in Meta.Files): og's migrator
	// over the app's driver adapter and the WASM schema engine.
	Program string `json:"program"`
	// Wasm is the packaged schema_engine_bg.wasm (in Meta.Files).
	Wasm string `json:"wasm"`
	// Push, when true, runs `prisma db push` (schemaPush: diff the schema
	// against the live database and apply it) instead of applying migrations
	// from Dir. Schema is the packaged .prisma schema file (in Meta.Files),
	// and AcceptDataLoss maps to schemaPush's force (apply destructive changes).
	Push           bool   `json:"push,omitempty"`
	AcceptDataLoss bool   `json:"acceptDataLoss,omitempty"`
	Schema         string `json:"schema,omitempty"`
}

// Handler returns an http.Handler serving the application in fsys. The
// process environment overrides the build-time environment.
func Handler(fsys fs.FS, logger *slog.Logger) (http.Handler, *Meta, error) {
	return handler(fsys, logger, nil)
}

// handler builds the application handler; console overrides where JS
// console output goes (nil: the process's stdout and stderr).
func handler(fsys fs.FS, logger *slog.Logger, console io.Writer) (http.Handler, *Meta, error) {
	var meta Meta
	if b, err := fs.ReadFile(fsys, MetaFile); err == nil {
		if err := json.Unmarshal(b, &meta); err != nil {
			return nil, nil, fmt.Errorf("app: %s: %w", MetaFile, err)
		}
		if meta.Format != FormatVersion {
			return nil, nil, fmt.Errorf("app: built for app format %d, but this runtime serves format %d (package the app with an og runtime from the same source as the og that built it)", meta.Format, FormatVersion)
		}
	}
	src, err := fs.ReadFile(fsys, ServerFile)
	if meta.Framework == "vite" && errors.Is(err, fs.ErrNotExist) {
		// A client-only Vite app.
		return viteHandler(fsys, &meta, nil), &meta, nil
	}
	if err != nil {
		return nil, nil, err
	}
	prog, err := sobek.Compile(ServerFile, string(src), false)
	if err != nil {
		return nil, nil, fmt.Errorf("app: compiling %s: %w", ServerFile, err)
	}
	env := map[string]string{}
	for k, v := range meta.Env {
		env[k] = v
	}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	// Process-wide state, shared by every request's isolated engine: the
	// app's KV store and the page/data cache.
	store := kv.NewMemory()
	cache := isr.NewCache()
	// The runtime's filesystem: the packaged project files (file tracing,
	// or a server program's outputs) under an in-memory layer that takes
	// every write, shared by every request's engine, lost at exit. SQLite
	// database files live in it too.
	lower := vfs.Empty
	if meta.Files != "" {
		sub, err := fs.Sub(fsys, meta.Files)
		if err != nil {
			return nil, nil, err
		}
		lower = sub
	}
	overlay := vfs.NewOverlay(lower)
	nativeServices := natives.NewServices() // sharp, better-sqlite3
	nativeServices.SQLite.Files = overlay
	// The pg pools stay warm across request engines (netpool's role, in Go
	// here); releasing them on teardown is the one process-wide cleanup the
	// native services need. SQLite's DBs are closed per engine.
	meta.Close = func() { nativeServices.Postgres.Close(); nativeServices.MySQL.Close() }
	sockets := nodeshim.NewSockets() // node:net, node:tls
	if err := seedPrerendered(fsys, cache); err != nil {
		return nil, nil, err
	}
	webAPI := webapi.Options{Env: env, Stdout: console, Stderr: console}
	exts := []func(*engine.Engine) error{
		func(e *engine.Engine) error { return kv.Install(e, store) },
		func(e *engine.Engine) error { return isr.Install(e, cache) },
		func(e *engine.Engine) error { return natives.Install(e, nativeServices) },
		func(e *engine.Engine) error { return nodeshim.InstallZlib(e) },
		func(e *engine.Engine) error { return nodeshim.InstallNet(e, sockets) },
		// The Go functions compiled into this binary (og:<name>).
		func(e *engine.Engine) error { return native.Install(e) },
		// The standard WebAssembly API (no WASI, no host access), created
		// lazily: e.g. next/og's resvg and yoga.
		func(e *engine.Engine) error {
			w, err := wasm.Install(e.VM, wasm.Options{Restricted: true, Stderr: io.Discard})
			if err != nil {
				return err
			}
			e.OnClose(w.Close)
			return nil
		},
		func(e *engine.Engine) error { return vfs.Install(e, overlay) },
	}
	// A Prisma project applies its migrations once, now, before any request:
	// the one-off engine shares the native services and the overlay, so the
	// migrated database is the one the request engines then read.
	if meta.PrismaMigrate != nil {
		if err := runPrismaMigrate(lower, &meta, webAPI, exts, env, logger); err != nil {
			// Release the native service pools the migrator opened (its pg/mysql
			// connection), so a failed migrate does not leak a connection or
			// block the caller's teardown.
			if meta.Close != nil {
				meta.Close()
			}
			return nil, nil, fmt.Errorf("app: prisma migrate deploy: %w", err)
		}
	}
	h := &server.Handler{
		Load:       server.ProgramLoader(prog, "__ogHandler"),
		WebAPI:     webAPI,
		Logger:     logger,
		Extensions: exts,
	}
	if meta.Files != "" && meta.Framework != "next" {
		h.Extensions = append(h.Extensions, startupOutputOnce(new(atomic.Bool)))
		// The program serves everything itself, static files included (as
		// its own middleware decides: sirv, compression, its base).
		return h, &meta, nil
	}
	if meta.Framework == "vite" {
		// A Vite app with a server (TanStack Start, or a server program):
		// client files first, then the server for everything else.
		return viteHandler(fsys, &meta, h), &meta, nil
	}
	isrHandler := &isr.Handler{Cache: cache, Next: h, Middleware: meta.HasMiddleware}
	cache.SetRevalidator(isrHandler.Revalidate)
	if b := meta.CacheBypass; b != nil && env[b.ValueEnv] != "" {
		name, value := b.Cookie, env[b.ValueEnv]
		isrHandler.Bypass = func(r *http.Request) bool {
			c, err := r.Cookie(name)
			return err == nil && c.Value == value
		}
	}
	app := withStatic(fsys, meta.BasePath, isrHandler)
	if meta.Images != nil {
		app = withImages(app, &meta, logger)
	}
	return app, &meta, nil
}

// startupOutputOnce shows a server program's startup output once per
// process, as Node runs the program once: the program runs in every
// request's engine, and prints its banner ("Listening on …") each time.
// The first engine prints everything; later ones drop console output until
// node:http dispatches the request (Symbol.for("og.startupDone")), so only
// what the request itself prints shows.
func startupOutputOnce(shown *atomic.Bool) func(*engine.Engine) error {
	return func(e *engine.Engine) error {
		quiet := shown.Swap(true)
		write, _ := sobek.AssertFunction(e.Host.Get("write"))
		if quiet && write != nil {
			if err := e.Host.Set("write", func(fc sobek.FunctionCall) sobek.Value {
				if quiet {
					return sobek.Undefined()
				}
				v, err := write(fc.This, fc.Arguments...)
				if err != nil {
					panic(err)
				}
				return v
			}); err != nil {
				return err
			}
		}
		install, err := e.VM.RunString(`(f) => Object.defineProperty(globalThis, Symbol.for("og.startupDone"), { value: f })`)
		if err != nil {
			return err
		}
		fn, _ := sobek.AssertFunction(install)
		_, err = fn(sobek.Undefined(), e.VM.ToValue(func() { quiet = false }))
		return err
	}
}

// PrerenderFile holds the build-time prerender (prerenderFile).
const PrerenderFile = "prerender.json"

// prerenderFile is PrerenderFile's format: cache entries rendered at build
// time, and the paths found to render dynamically.
type prerenderFile struct {
	Entries     map[string]*isr.Entry       `json:"entries"`
	Dynamic     []string                    `json:"dynamic"`
	StaticPaths map[string]*isr.StaticPaths `json:"staticPaths,omitempty"`
}

func seedPrerendered(fsys fs.FS, cache *isr.Cache) error {
	b, err := fs.ReadFile(fsys, PrerenderFile)
	if err != nil {
		return nil // nothing prerendered
	}
	var pre prerenderFile
	if err := json.Unmarshal(b, &pre); err != nil {
		return fmt.Errorf("app: %s: %w", PrerenderFile, err)
	}
	cache.Seed(pre.Entries)
	cache.SeedDynamic(pre.Dynamic)
	cache.SeedStaticPaths(pre.StaticPaths)
	return nil
}
