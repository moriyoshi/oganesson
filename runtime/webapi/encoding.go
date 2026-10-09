package webapi

import (
	"strings"
	"unicode/utf8"

	"github.com/grafana/sobek"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"
)

func (h *host) installEncoding() {
	h.set("utf8Encode", func(s string) sobek.Value { return h.buffer([]byte(s)) })
	// newDecoder(label, fatal) -> {name, decode(bytes, stream)} or null for an
	// unknown label. BOM handling stays in JS.
	h.set("newDecoder", func(label string, fatal bool) sobek.Value {
		name, dec := lookupDecoder(label, fatal)
		if dec == nil {
			return sobek.Null()
		}
		obj := h.vm.NewObject()
		_ = obj.Set("name", name)
		_ = obj.Set("decode", func(fc sobek.FunctionCall) sobek.Value {
			s, ok := dec.decode(h.bytes(fc.Argument(0)), fc.Argument(1).ToBoolean())
			if !ok {
				panic(h.vm.NewTypeError("The encoded data was not valid for encoding " + name))
			}
			return h.vm.ToValue(s)
		})
		return obj
	})
}

type decoder interface {
	// decode consumes input; with stream set, trailing partial sequences are
	// buffered for the next call. ok is false on a fatal error.
	decode(in []byte, stream bool) (out string, ok bool)
}

func lookupDecoder(label string, fatal bool) (string, decoder) {
	label = strings.ToLower(strings.Trim(label, " \t\n\f\r"))
	enc, err := htmlindex.Get(label)
	if err != nil {
		return "", nil
	}
	name, _ := htmlindex.Name(enc)
	if name == "utf-8" {
		return name, &utf8Decoder{fatal: fatal}
	}
	if name == "replacement" {
		return "", nil // per the Encoding spec, TextDecoder rejects it
	}
	return name, &xtextDecoder{t: enc.NewDecoder(), enc: enc}
}

// utf8Decoder implements the WHATWG UTF-8 decoder, which replaces each
// maximal invalid subpart with a single U+FFFD (Go's decoder does not).
type utf8Decoder struct {
	fatal   bool
	pending []byte
}

func (d *utf8Decoder) decode(in []byte, stream bool) (string, bool) {
	buf := append(d.pending, in...)
	d.pending = nil
	var sb strings.Builder
	for i := 0; i < len(buf); {
		c := buf[i]
		if c < 0x80 {
			sb.WriteByte(c)
			i++
			continue
		}
		need, lower, upper := 0, byte(0x80), byte(0xBF)
		switch {
		case c >= 0xC2 && c <= 0xDF:
			need = 1
		case c >= 0xE0 && c <= 0xEF:
			need = 2
			if c == 0xE0 {
				lower = 0xA0
			} else if c == 0xED {
				upper = 0x9F
			}
		case c >= 0xF0 && c <= 0xF4:
			need = 3
			if c == 0xF0 {
				lower = 0x90
			} else if c == 0xF4 {
				upper = 0x8F
			}
		default:
			if d.fatal {
				return "", false
			}
			sb.WriteRune(utf8.RuneError)
			i++
			continue
		}
		j := i + 1
		valid := true
		for k := 0; k < need; k++ {
			if j >= len(buf) {
				if stream {
					d.pending = append([]byte(nil), buf[i:]...)
					return sb.String(), true
				}
				valid = false
				break
			}
			b := buf[j]
			lo, hi := byte(0x80), byte(0xBF)
			if k == 0 {
				lo, hi = lower, upper
			}
			if b < lo || b > hi {
				valid = false
				break
			}
			j++
		}
		if !valid {
			if d.fatal {
				return "", false
			}
			sb.WriteRune(utf8.RuneError)
			i = j // skip the maximal subpart; the offending byte is reprocessed
			continue
		}
		r, _ := utf8.DecodeRune(buf[i:j])
		sb.WriteRune(r)
		i = j
	}
	return sb.String(), true
}

// xtextDecoder handles legacy encodings via golang.org/x/text. Fatal mode is
// best-effort: x/text always substitutes.
type xtextDecoder struct {
	t       *encoding.Decoder
	enc     encoding.Encoding
	pending []byte
}

func (d *xtextDecoder) decode(in []byte, stream bool) (string, bool) {
	src := append(d.pending, in...)
	d.pending = nil
	dst := make([]byte, len(src)*3+16)
	var out []byte
	for {
		n, m, err := d.t.Transform(dst, src, !stream)
		out = append(out, dst[:n]...)
		src = src[m:]
		switch err {
		case nil:
			if !stream {
				d.t.Reset()
			}
			return string(out), true
		case transform.ErrShortDst:
			continue
		case transform.ErrShortSrc:
			d.pending = append([]byte(nil), src...)
			return string(out), true
		default:
			return string(out), false
		}
	}
}
