// Package webapi installs the WinterCG-style Web API surface on an engine:
// timers, console, events, AbortController, encoding, URL, streams, Blob,
// FormData, Headers/Request/Response, fetch, crypto, structuredClone and the
// built-in polyfills Goja lacks.
//
// The JS half lives in js/src (TypeScript). Go generate bundles a prelude for
// each engine. The Go half registers primitives on the engine's host object.
package webapi

//go:generate go run ./internal/genemoji
//go:generate go run ./internal/genprelude

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/moriyoshi/oganesson/internal/jsvm"
	"github.com/moriyoshi/oganesson/runtime/engine"
)

var preludeProgram = jsvm.MustCompile("og:prelude", preludeSource, true)

// Options configure the installed APIs.
type Options struct {
	// Stdout and Stderr receive console output. Default to os.Stdout/Stderr.
	Stdout, Stderr io.Writer
	// HTTPClient performs outbound fetch(). Defaults to http.DefaultClient
	// without automatic redirects (fetch follows them itself).
	HTTPClient *http.Client
	// Env is exposed as process.env.
	Env map[string]string
	// BaseURL, if set, resolves relative URLs given to Request, fetch and
	// Response.redirect (a browser's "API base URL"). Servers leave it empty:
	// relative URLs then throw, as in edge runtimes.
	BaseURL string
}

// Runtime is the installed Web API layer of one engine.
type Runtime struct {
	h *host
	// exports holds JS helpers the prelude hands back to Go.
	exports *jsvm.Object
}

// Install registers the host primitives and runs the prelude. It must be
// called before any user code runs on the engine.
func Install(e *engine.Engine, opts Options) (*Runtime, error) {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	h := &host{e: e, vm: e.VM, opts: opts}
	h.installConsole()
	h.installEncoding()
	h.installCompression()
	h.installURL()
	h.installCrypto()
	h.installFetch()
	h.installEnv()
	if !jsvm.UsesEkuma {
		h.installCollator()
		h.installStringProperties()
	}
	h.set("abDetach", func(b jsvm.ArrayBuffer) bool { return b.Detach() })
	h.set("abDetached", func(b jsvm.ArrayBuffer) bool { return b.Detached() })
	h.set("baseURL", opts.BaseURL)

	v, err := e.VM.RunProgram(preludeProgram)
	if err != nil {
		return nil, fmt.Errorf("webapi: prelude: %w", err)
	}
	install, ok := jsvm.AssertFunction(v)
	if !ok {
		return nil, fmt.Errorf("webapi: prelude did not evaluate to a function")
	}
	exports, err := install(jsvm.Undefined(), e.Host)
	if err != nil {
		return nil, fmt.Errorf("webapi: prelude install: %w", err)
	}
	return &Runtime{h: h, exports: exports.ToObject(e.VM)}, nil
}

type host struct {
	e    *engine.Engine
	vm   *jsvm.Runtime
	opts Options
	st   *bodyState
}

func (h *host) set(name string, v any) {
	if err := h.e.Host.Set(name, v); err != nil {
		panic(err)
	}
}

// bytes returns a copy of a BufferSource-like value's bytes. The JS side
// normalizes inputs to Uint8Array before calling into Go.
func (h *host) bytes(v jsvm.Value) []byte {
	if v == nil || jsvm.IsUndefined(v) || jsvm.IsNull(v) {
		return nil
	}
	switch b := v.Export().(type) {
	case []byte:
		return append([]byte(nil), b...)
	case jsvm.ArrayBuffer:
		return append([]byte(nil), b.Bytes()...)
	case string:
		return []byte(b)
	}
	panic(h.vm.NewTypeError("expected a Uint8Array"))
}

// buffer converts Go bytes into a fresh ArrayBuffer.
func (h *host) buffer(b []byte) jsvm.Value {
	return h.vm.ToValue(h.vm.NewArrayBuffer(b))
}

func (h *host) installConsole() {
	h.set("write", func(stream int, s string) {
		w := h.opts.Stdout
		if stream == 2 {
			w = h.opts.Stderr
		}
		_, _ = io.WriteString(w, s)
	})
}

func (h *host) installEnv() {
	env := h.vm.NewObject()
	for k, v := range h.opts.Env {
		_ = env.Set(k, v)
	}
	h.set("env", env)
	h.set("pid", os.Getpid())
	h.set("ppid", os.Getppid())
}
