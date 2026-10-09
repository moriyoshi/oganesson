// Package server serves HTTP requests with a JS fetch handler, creating a
// fresh, fully isolated engine for every request.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/runtime/engine"
	"github.com/moriyoshi/oganesson/runtime/webapi"
)

// LoadFunc evaluates the application in a fresh engine and returns its fetch
// handler: (request: Request) => Response | Promise<Response>.
type LoadFunc func(e *engine.Engine) (sobek.Callable, error)

// ProgramLoader returns a LoadFunc that runs a precompiled script and reads
// the handler from the global named export (e.g. "__ogHandler").
func ProgramLoader(p *sobek.Program, export string) LoadFunc {
	return func(e *engine.Engine) (sobek.Callable, error) {
		if _, err := e.VM.RunProgram(p); err != nil {
			return nil, err
		}
		fn, ok := sobek.AssertFunction(e.VM.Get(export))
		if !ok {
			return nil, fmt.Errorf("server: global %q is not a function", export)
		}
		return fn, nil
	}
}

// Handler is an http.Handler backed by a JS fetch handler.
type Handler struct {
	Load   LoadFunc
	WebAPI webapi.Options
	// Timeout bounds producing a response, streaming included. Default 60s.
	Timeout time.Duration
	// Grace bounds background work after the response ends. Default 5s.
	Grace  time.Duration
	Logger *slog.Logger
	// Extensions install additional host APIs (e.g. the KV store) into
	// every request's engine, before the application loads.
	Extensions []func(*engine.Engine) error
}

func (h *Handler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	timeout, grace := h.Timeout, h.Grace
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	if grace == 0 {
		grace = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	log := h.logger().With("method", r.Method, "path", r.URL.Path)

	e := engine.New(engine.Options{Uncaught: func(err error) error {
		log.Error("uncaught exception", "error", err)
		return nil
	}})
	defer e.Close()

	fail := func(stage string, err error, headerSent bool) {
		// A JS exception's message carries only its first frame; the log
		// gets the whole stack.
		var detail any = err
		if ex := (*sobek.Exception)(nil); errors.As(err, &ex) {
			detail = ex.String()
		}
		log.Error("request failed", "stage", stage, "error", detail)
		if !headerSent {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	}

	rt, err := webapi.Install(e, h.WebAPI)
	if err != nil {
		fail("install", err, false)
		return
	}
	for _, ext := range h.Extensions {
		if err := ext(e); err != nil {
			fail("install", err, false)
			return
		}
	}
	handler, err := h.Load(e)
	if err != nil {
		fail("load", err, false)
		return
	}
	req, abort, err := rt.IncomingRequest(r)
	if err != nil {
		fail("request", err, false)
		return
	}
	// Client went away: abort the request signal on the loop goroutine.
	stop := context.AfterFunc(r.Context(), func() {
		e.Loop.Post(func(*sobek.Runtime) error { abort(); return nil })
	})
	defer stop()

	ret, err := handler(sobek.Undefined(), req)
	if err != nil {
		fail("handler", err, false)
		return
	}
	resp, err := e.Await(ctx, ret)
	if err != nil {
		fail("handler", err, false)
		return
	}
	if sent, err := rt.WriteResponse(ctx, w, resp); err != nil {
		fail("response", err, sent)
		return
	}

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), grace)
	defer cancelDrain()
	if err := e.Drain(drainCtx); err != nil && !errors.Is(err, engine.ErrInterrupted) {
		log.Warn("background work failed", "error", err)
	}
}
