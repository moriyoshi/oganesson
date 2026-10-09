package webapi

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/grafana/sobek"
)

const bodyChunkSize = 64 << 10

// bodyState tracks in-flight fetches and open Go-side bodies (fetch
// responses and incoming server requests) per engine. JS reads them with
// bodyRead/bodyClose.
type bodyState struct {
	nextID  int64
	cancels map[int64]context.CancelFunc
	bodies  map[int64]io.ReadCloser
}

// addBody registers a Go reader for JS consumption and returns its id.
func (h *host) addBody(r io.ReadCloser) int64 {
	h.st.nextID++
	h.st.bodies[h.st.nextID] = r
	return h.st.nextID
}

// installFetch registers a single-hop HTTP transport. Redirect handling,
// CORS-free fetch semantics and body streams are implemented in JS.
//
//	fetchStart({method, url, headers: [[k, v]...], body: Uint8Array|null}) -> [id, Promise<{status, statusText, url, headers, bodyId}>]
//	fetchAbort(id)
//	bodyRead(bodyId) -> Promise<ArrayBuffer|null>   (null at EOF)
//	bodyClose(bodyId)
func (h *host) installFetch() {
	st := &bodyState{cancels: map[int64]context.CancelFunc{}, bodies: map[int64]io.ReadCloser{}}
	h.st = st
	h.e.OnClose(func() {
		for _, c := range st.cancels {
			c()
		}
		for _, b := range st.bodies {
			_ = b.Close()
		}
	})

	h.set("fetchStart", func(spec *sobek.Object) sobek.Value {
		st.nextID++
		id := st.nextID
		ctx, cancel := context.WithCancel(context.Background())
		st.cancels[id] = cancel

		var body io.Reader
		if b := spec.Get("body"); b != nil && !sobek.IsNull(b) && !sobek.IsUndefined(b) {
			body = bytes.NewReader(h.bytes(b))
		}
		req, err := http.NewRequestWithContext(ctx, spec.Get("method").String(), spec.Get("url").String(), body)
		promise, resolve, reject := h.vm.NewPromise()
		if err != nil {
			delete(st.cancels, id)
			cancel()
			_ = reject(h.vm.NewTypeError("fetch failed: " + err.Error()))
			return h.vm.ToValue([]any{id, promise})
		}
		if hs, ok := spec.Get("headers").Export().([]any); ok {
			for _, pair := range hs {
				kv := pair.([]any)
				req.Header.Add(kv[0].(string), kv[1].(string))
			}
		}
		if req.Header.Get("User-Agent") == "" {
			req.Header.Set("User-Agent", "Oganesson")
		}

		complete := h.e.Loop.Async()
		go func() {
			resp, err := h.opts.HTTPClient.Do(req)
			complete(func(vm *sobek.Runtime) error {
				delete(st.cancels, id)
				if err != nil {
					cancel()
					return reject(vm.NewTypeError("fetch failed: " + err.Error()))
				}
				bodyID := h.addBody(&cancelOnClose{ReadCloser: resp.Body, cancel: cancel})
				headers := make([]any, 0, len(resp.Header))
				for k, vs := range resp.Header {
					for _, v := range vs {
						headers = append(headers, []any{k, v})
					}
				}
				r := vm.NewObject()
				_ = r.Set("status", resp.StatusCode)
				_ = r.Set("statusText", http.StatusText(resp.StatusCode))
				_ = r.Set("url", req.URL.String())
				_ = r.Set("headers", headers)
				_ = r.Set("bodyId", bodyID)
				return resolve(r)
			})
		}()
		return h.vm.ToValue([]any{id, promise})
	})

	h.set("fetchAbort", func(id int64) {
		if c, ok := st.cancels[id]; ok {
			c()
		}
	})

	h.set("bodyRead", func(id int64) sobek.Value {
		promise, resolve, reject := h.vm.NewPromise()
		body, ok := st.bodies[id]
		if !ok {
			_ = resolve(sobek.Null())
			return h.vm.ToValue(promise)
		}
		complete := h.e.Loop.Async()
		go func() {
			buf := make([]byte, bodyChunkSize)
			n, err := body.Read(buf)
			complete(func(vm *sobek.Runtime) error {
				if n > 0 {
					// Deliver data first; a trailing error surfaces on the next read.
					return resolve(vm.NewArrayBuffer(buf[:n]))
				}
				delete(st.bodies, id)
				_ = body.Close()
				if err == io.EOF || err == nil {
					return resolve(sobek.Null())
				}
				return reject(vm.NewTypeError("network error while reading body: " + err.Error()))
			})
		}()
		return h.vm.ToValue(promise)
	})

	h.set("bodyClose", func(id int64) {
		if b, ok := st.bodies[id]; ok {
			delete(st.bodies, id)
			_ = b.Close()
		}
	})
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
