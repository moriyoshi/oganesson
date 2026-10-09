package webapi

import (
	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/runtime/codec"
)

// installCompression backs CompressionStream / DecompressionStream with
// runtime/codec's streaming codecs: compressor(format, decompress) returns
// {write(bytes), finish()}, each returning the output produced so far.
// Decompression's write returns [output, error message or null]: output
// decoded before an error (input after the end) is still delivered.
func (h *host) installCompression() {
	h.set("compressor", func(format string, decompress bool) sobek.Value {
		obj := h.vm.NewObject()
		fail := func(err error) string {
			return "DecompressionStream: invalid " + format + " data: " + err.Error()
		}
		if decompress {
			d, err := codec.NewDecoder(format)
			if err != nil {
				panic(h.vm.NewTypeError(err.Error()))
			}
			h.e.OnClose(d.Stop)
			_ = obj.Set("write", func(fc sobek.FunctionCall) sobek.Value {
				out, err := d.Feed(h.bytes(fc.Argument(0)))
				msg := sobek.Null()
				if err != nil {
					msg = h.vm.ToValue(fail(err))
				}
				return h.vm.ToValue([]any{h.buffer(out), msg})
			})
			_ = obj.Set("finish", func() sobek.Value {
				out, err := d.Close()
				if err != nil {
					panic(h.vm.NewTypeError(fail(err)))
				}
				return h.buffer(out)
			})
			return obj
		}
		c, err := codec.NewCompressor(format, -1)
		if err != nil {
			panic(h.vm.NewTypeError(err.Error()))
		}
		_ = obj.Set("write", func(fc sobek.FunctionCall) sobek.Value { return h.buffer(c.Write(h.bytes(fc.Argument(0)))) })
		_ = obj.Set("finish", func() sobek.Value { return h.buffer(c.Close()) })
		return obj
	})
}
