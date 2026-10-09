package app

import (
	"bytes"
	"fmt"
	"github.com/moriyoshi/oganesson/runtime/engine"
	"github.com/moriyoshi/oganesson/runtime/webapi"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
)

// TestFormatVersion: a runtime refuses apps built for another app format
// (e.g. packaged with an og runtime from different source).
func TestFormatVersion(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, meta := range []string{`{"framework":"next"}`, `{"format":999,"framework":"next"}`, fmt.Sprintf(`{"format":%d,"framework":"next"}`, FormatVersion-1)} {
		fsys := fstest.MapFS{ServerFile: {Data: []byte(`globalThis.__ogHandler = () => new Response("ok");`)}, MetaFile: {Data: []byte(meta)}}
		if _, _, err := Handler(fsys, logger); err == nil || !strings.Contains(err.Error(), "app format") {
			t.Errorf("%s: err = %v", meta, err)
		}
	}
	fsys := fstest.MapFS{ServerFile: {Data: []byte(`globalThis.__ogHandler = () => new Response("ok");`)}, MetaFile: {Data: []byte(fmt.Sprintf(`{"format":%d,"framework":"next"}`, FormatVersion))}}
	if _, _, err := Handler(fsys, logger); err != nil {
		t.Errorf("current format rejected: %v", err)
	}
}

// TestStartupOutputOnce: the first engine prints its program's startup
// output; later engines drop console output until node:http dispatches the
// request (og.startupDone), then print as usual.
func TestStartupOutputOnce(t *testing.T) {
	var out bytes.Buffer
	ext := startupOutputOnce(new(atomic.Bool))
	run := func(js string) {
		e := engine.New(engine.Options{})
		defer e.Close()
		if _, err := webapi.Install(e, webapi.Options{Stdout: &out, Stderr: &out}); err != nil {
			t.Fatal(err)
		}
		if err := ext(e); err != nil {
			t.Fatal(err)
		}
		if _, err := e.VM.RunString(js); err != nil {
			t.Fatal(err)
		}
	}
	program := `console.log("banner"); console.error("warn");`
	run(program + `globalThis[Symbol.for("og.startupDone")](); console.log("request 1");`)
	run(program + `globalThis[Symbol.for("og.startupDone")](); console.log("request 2");`)
	run(program + `globalThis[Symbol.for("og.startupDone")](); console.log("request 3");`)
	if got, want := out.String(), "banner\nwarn\nrequest 1\nrequest 2\nrequest 3\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
