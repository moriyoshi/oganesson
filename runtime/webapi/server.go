package webapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/grafana/sobek"
)

// IncomingRequest converts r into a JS Request whose body streams from
// r.Body. abort aborts the request's signal (e.g. on client disconnect).
func (rt *Runtime) IncomingRequest(r *http.Request) (req sobek.Value, abort func(), err error) {
	vm := rt.h.vm
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	headers := make([]any, 0, len(r.Header)+1)
	if r.Host != "" {
		headers = append(headers, []any{"host", r.Host})
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			headers = append(headers, []any{k, v})
		}
	}
	var bodyID int64
	if r.Body != nil && r.Body != http.NoBody && r.Method != http.MethodGet && r.Method != http.MethodHead {
		bodyID = rt.h.addBody(io.NopCloser(r.Body))
	}
	spec := vm.NewObject()
	_ = spec.Set("method", r.Method)
	_ = spec.Set("url", scheme+"://"+r.Host+r.URL.RequestURI())
	_ = spec.Set("headers", headers)
	_ = spec.Set("bodyId", bodyID)

	fn, _ := sobek.AssertFunction(rt.exports.Get("incomingRequest"))
	res, err := fn(sobek.Undefined(), spec)
	if err != nil {
		return nil, nil, err
	}
	obj := res.ToObject(vm)
	abortFn, _ := sobek.AssertFunction(obj.Get("abort"))
	return obj.Get("request"), func() { _, _ = abortFn(sobek.Undefined()) }, nil
}

// ErrBadResponse reports a handler result that is not a usable Response.
var ErrBadResponse = errors.New("webapi: handler did not return a valid Response")

// WriteResponse streams a JS Response to w, flushing after every chunk so
// that streamed HTML and RSC payloads reach the client incrementally.
// headerSent reports whether the status line went out before any error.
func (rt *Runtime) WriteResponse(ctx context.Context, w http.ResponseWriter, resp sobek.Value) (headerSent bool, err error) {
	vm := rt.h.vm
	infoFn, _ := sobek.AssertFunction(rt.exports.Get("responseInfo"))
	iv, err := infoFn(sobek.Undefined(), resp)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrBadResponse, err)
	}
	info := iv.ToObject(vm)
	if info.Get("type").String() == "error" {
		return false, fmt.Errorf("%w: Response.error()", ErrBadResponse)
	}
	h := w.Header()
	if hs, ok := info.Get("headers").Export().([]any); ok {
		for _, pair := range hs {
			kv := pair.([]any)
			h.Add(kv[0].(string), kv[1].(string))
		}
	}
	// No Content-Type stays none, as Node sends it: net/http would sniff
	// one from the body.
	if _, ok := h["Content-Type"]; !ok {
		h["Content-Type"] = nil
	}
	w.WriteHeader(int(info.Get("status").ToInteger()))

	next, ok := sobek.AssertFunction(info.Get("next"))
	if !ok {
		return true, nil // null body
	}
	cancel, _ := sobek.AssertFunction(info.Get("cancel"))
	flusher, _ := w.(http.Flusher)
	for {
		p, err := next(sobek.Undefined())
		if err != nil {
			return true, err
		}
		chunk, err := rt.h.e.Await(ctx, p)
		if err != nil {
			_, _ = cancel(sobek.Undefined())
			return true, err
		}
		if sobek.IsNull(chunk) {
			return true, nil
		}
		b, _ := chunk.Export().([]byte)
		if _, err := w.Write(b); err != nil {
			_, _ = cancel(sobek.Undefined())
			return true, err
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}
