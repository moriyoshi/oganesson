package nodeshim

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"

	"github.com/andybalholm/brotli"

	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/runtime/codec"
	"github.com/moriyoshi/oganesson/runtime/engine"
)

// InstallZlib provides the compression primitives behind js/zlib.js (Go's
// compress/*), for the build host and request VMs.
func InstallZlib(e *engine.Engine) error {
	vm := e.VM
	o := vm.NewObject()
	compress := func(mk func(io.Writer, int) (io.WriteCloser, error)) func([]byte, int) sobek.Value {
		return func(in []byte, level int) sobek.Value {
			var buf bytes.Buffer
			w, err := mk(&buf, level)
			if err == nil {
				_, err = w.Write(in)
				if cerr := w.Close(); err == nil {
					err = cerr
				}
			}
			if err != nil {
				panic(vm.NewGoError(err))
			}
			return vm.ToValue(vm.NewArrayBuffer(buf.Bytes()))
		}
	}
	decompress := func(mk func(io.Reader) (io.Reader, error)) func([]byte, int) sobek.Value {
		return func(in []byte, _ int) sobek.Value {
			r, err := mk(bytes.NewReader(in))
			var out []byte
			if err == nil {
				out, err = io.ReadAll(r)
			}
			if err != nil {
				panic(vm.NewGoError(err))
			}
			return vm.ToValue(vm.NewArrayBuffer(out))
		}
	}
	_ = o.Set("gzip", compress(func(w io.Writer, l int) (io.WriteCloser, error) { return gzip.NewWriterLevel(w, l) }))
	_ = o.Set("gunzip", decompress(func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) }))
	_ = o.Set("deflate", compress(func(w io.Writer, l int) (io.WriteCloser, error) { return zlib.NewWriterLevel(w, l) }))
	_ = o.Set("inflate", decompress(func(r io.Reader) (io.Reader, error) { return zlib.NewReader(r) }))
	_ = o.Set("deflateRaw", compress(func(w io.Writer, l int) (io.WriteCloser, error) { return flate.NewWriter(w, l) }))
	_ = o.Set("inflateRaw", decompress(func(r io.Reader) (io.Reader, error) { return flate.NewReader(r), nil }))
	// Brotli (Node always has it; Go's standard library does not):
	// andybalholm/brotli. level is the quality (Node's default is 11).
	_ = o.Set("brotliCompress", compress(func(w io.Writer, l int) (io.WriteCloser, error) {
		if l < 0 || l > 11 {
			l = brotli.BestCompression
		}
		return brotli.NewWriterLevel(w, l), nil
	}))
	_ = o.Set("brotliDecompress", decompress(func(r io.Reader) (io.Reader, error) { return brotli.NewReader(r), nil }))
	// stream(kind, level): a streaming codec for zlib's stream classes
	// (runtime/codec): {write(bytes), flush(), finish()} each return the
	// output produced so far; decompressors throw on invalid input.
	formats := map[string]string{
		"gzip": "gzip", "deflate": "deflate", "deflateRaw": "deflate-raw", "brotliCompress": "brotli",
		"gunzip": "gzip", "inflate": "deflate", "inflateRaw": "deflate-raw", "brotliDecompress": "brotli", "unzip": "unzip",
	}
	_ = o.Set("stream", func(kind string, level int) sobek.Value {
		format, ok := formats[kind]
		if !ok {
			panic(vm.NewTypeError("og: unknown zlib stream " + kind))
		}
		obj := vm.NewObject()
		buf := func(b []byte) sobek.Value { return vm.ToValue(vm.NewArrayBuffer(b)) }
		check := func(b []byte, err error) sobek.Value {
			if err != nil {
				panic(vm.NewGoError(err))
			}
			return buf(b)
		}
		switch kind {
		case "gzip", "deflate", "deflateRaw", "brotliCompress":
			c, err := codec.NewCompressor(format, level)
			if err != nil {
				panic(vm.NewGoError(err))
			}
			_ = obj.Set("write", func(in []byte) sobek.Value { return buf(c.Write(in)) })
			_ = obj.Set("flush", func() sobek.Value { return buf(c.Flush()) })
			_ = obj.Set("finish", func() sobek.Value { return buf(c.Close()) })
		default:
			d, err := codec.NewDecoder(format)
			if err != nil {
				panic(vm.NewGoError(err))
			}
			e.OnClose(d.Stop)
			_ = obj.Set("write", func(in []byte) sobek.Value { return check(d.Feed(in)) })
			_ = obj.Set("flush", func() sobek.Value { return check(d.Feed(nil)) })
			_ = obj.Set("finish", func() sobek.Value { return check(d.Close()) })
		}
		return obj
	})
	install, err := vm.RunString(`(function (o) { Object.defineProperty(globalThis, Symbol.for("og.zlib"), { value: o }); })`)
	if err != nil {
		return err
	}
	fn, _ := sobek.AssertFunction(install)
	_, err = fn(sobek.Undefined(), o)
	return err
}
