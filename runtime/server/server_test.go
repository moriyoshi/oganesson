package server_test

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/runtime/server"
	"github.com/moriyoshi/oganesson/runtime/webapi"
)

const app = `
globalThis.__ogHandler = async function (req) {
  const url = new URL(req.url);
  switch (url.pathname) {
    case "/echo":
      return new Response(req.body, { headers: { "x-method": req.method, "x-host": req.headers.get("host") } });
    case "/stream": {
      let i = 0;
      return new Response(new ReadableStream({
        async pull(c) {
          if (i === 3) { c.close(); return; }
          await new Promise((r) => setTimeout(r, 30));
          c.enqueue(new TextEncoder().encode("chunk" + i++ + "\n"));
        },
      }), { headers: { "content-type": "text/plain" } });
    }
    case "/leak": {
      const before = globalThis.counter ?? 0;
      globalThis.counter = before + 1;
      return new Response(String(before));
    }
    case "/untyped":
      return new Response(new TextEncoder().encode("<!doctype html><p>hi</p>"));
    case "/throw":
      throw new Error("boom");
    case "/notresponse":
      return { status: 200 };
    case "/background":
      setTimeout(() => console.log("background done"), 10);
      return new Response("ok", { status: 202, headers: [["set-cookie", "a=1"], ["set-cookie", "b=2"]] });
  }
  return new Response("not found", { status: 404 });
};`

func newServer(t *testing.T, out io.Writer) *httptest.Server {
	t.Helper()
	prog := sobek.MustCompile("app.js", app, false)
	h := &server.Handler{
		Load:   server.ProgramLoader(prog, "__ogHandler"),
		WebAPI: webapi.Options{Stdout: out},
	}
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func TestEchoAndHeaders(t *testing.T) {
	s := newServer(t, io.Discard)
	resp, err := http.Post(s.URL+"/echo", "text/plain", strings.NewReader("hello body"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "hello body" || resp.Header.Get("x-method") != "POST" || !strings.HasPrefix(resp.Header.Get("x-host"), "127.0.0.1:") {
		t.Fatalf("got %q headers %v", b, resp.Header)
	}
}

// A response without a Content-Type goes out without one, as Node sends
// it (net/http would sniff text/html from this body).
func TestNoContentTypeSniffing(t *testing.T) {
	s := newServer(t, io.Discard)
	resp, err := http.Get(s.URL + "/untyped")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if _, ok := resp.Header["Content-Type"]; ok || string(b) != "<!doctype html><p>hi</p>" {
		t.Fatalf("content-type %q, body %q", resp.Header.Get("Content-Type"), b)
	}
}

func TestStreamingIsIncremental(t *testing.T) {
	s := newServer(t, io.Discard)
	start := time.Now()
	resp, err := http.Get(s.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	r := bufio.NewReader(resp.Body)
	first, _ := r.ReadString('\n')
	firstAt := time.Since(start)
	rest, _ := io.ReadAll(r)
	total := time.Since(start)
	if first != "chunk0\n" || string(rest) != "chunk1\nchunk2\n" {
		t.Fatalf("body = %q + %q", first, rest)
	}
	if firstAt > total-40*time.Millisecond {
		t.Fatalf("first chunk arrived at %v of %v: not streamed", firstAt, total)
	}
}

func TestRequestsAreIsolated(t *testing.T) {
	s := newServer(t, io.Discard)
	for i := 0; i < 3; i++ {
		resp, err := http.Get(s.URL + "/leak")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		if string(b) != "0" {
			t.Fatalf("request %d saw state from a previous request: %q", i, b)
		}
	}
}

func TestErrorsBecome500(t *testing.T) {
	s := newServer(t, io.Discard)
	for _, p := range []string{"/throw", "/notresponse"} {
		resp, err := http.Get(s.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 500 {
			t.Errorf("%s: status %d", p, resp.StatusCode)
		}
	}
}

func TestBackgroundWorkAndSetCookie(t *testing.T) {
	out := &syncWriter{}
	s := newServer(t, out)
	resp, err := http.Get(s.URL + "/background")
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	if resp.StatusCode != 202 || len(resp.Header.Values("set-cookie")) != 2 {
		t.Fatalf("status %d cookies %v", resp.StatusCode, resp.Header.Values("set-cookie"))
	}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), "background done") {
		if time.Now().After(deadline) {
			t.Fatal("background timer never ran")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// syncWriter guards a builder written by the server goroutine and polled
// by the test.
type syncWriter struct {
	mu sync.Mutex
	w  strings.Builder
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func (s *syncWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.String()
}
