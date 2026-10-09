// Package codec holds streaming compressors and decompressors (Go's
// compress/* and andybalholm/brotli) for the JavaScript runtimes:
// runtime/webapi's CompressionStream / DecompressionStream and
// runtime/nodeshim's zlib streams. Each call returns the output produced
// so far, so output streams as input arrives.
package codec

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/andybalholm/brotli"
)

// Formats: "gzip", "deflate" (zlib), "deflate-raw", "brotli"; decoders
// also take "unzip" (gzip or zlib, by the header, as Node's).

// Compressor streams input through an encoder.
type Compressor struct {
	buf bytes.Buffer
	w   interface {
		io.WriteCloser
		Flush() error
	}
}

// NewCompressor returns a compressor for format at level (-1: the
// format's default; brotli's quality, 0-11).
func NewCompressor(format string, level int) (*Compressor, error) {
	c := &Compressor{}
	var err error
	switch format {
	case "gzip":
		if level < 0 {
			level = gzip.DefaultCompression
		}
		c.w, err = gzip.NewWriterLevel(&c.buf, level)
	case "deflate":
		if level < 0 {
			level = zlib.DefaultCompression
		}
		c.w, err = zlib.NewWriterLevel(&c.buf, level)
	case "deflate-raw":
		if level < 0 {
			level = flate.DefaultCompression
		}
		c.w, err = flate.NewWriter(&c.buf, level)
	case "brotli":
		if level < 0 || level > brotli.BestCompression {
			level = brotli.BestCompression
		}
		c.w = brotli.NewWriterLevel(&c.buf, level)
	default:
		return nil, fmt.Errorf("unsupported compression format %q", format)
	}
	return c, err
}

func (c *Compressor) take() []byte {
	out := append([]byte(nil), c.buf.Bytes()...)
	c.buf.Reset()
	return out
}

// Write compresses p, returning the output the encoder has emitted.
func (c *Compressor) Write(p []byte) []byte {
	_, _ = c.w.Write(p)
	return c.take()
}

// Flush emits everything written so far (a sync flush).
func (c *Compressor) Flush() []byte {
	_ = c.w.Flush()
	return c.take()
}

// Close ends the stream, returning the rest.
func (c *Compressor) Close() []byte {
	_ = c.w.Close()
	return c.take()
}

// Decoder runs a Go decoder (which pulls its input) on a goroutine over the
// chunks written so far: Write hands it a chunk and returns the output once
// the decoder waits for more input; Close ends the input and returns the
// rest. Input after the end of the compressed stream is an error.
type Decoder struct {
	mu      sync.Mutex
	cond    *sync.Cond
	chunks  [][]byte
	eof     bool // no more input (Close or Stop)
	starved bool // the decoder waits for input
	done    bool // the decoder returned
	err     error
	out     bytes.Buffer
}

// NewDecoder starts a decoder for format.
func NewDecoder(format string) (*Decoder, error) {
	switch format {
	case "gzip", "deflate", "deflate-raw", "brotli", "unzip":
	default:
		return nil, fmt.Errorf("unsupported compression format %q", format)
	}
	d := &Decoder{}
	d.cond = sync.NewCond(&d.mu)
	go d.run(format)
	return d, nil
}

func (d *Decoder) run(format string) {
	var r io.Reader
	var err error
	if format == "unzip" {
		// Node's unzip: gzip by its magic number, else zlib.
		var b byte
		if b, err = d.ReadByte(); err == nil {
			d.mu.Lock()
			d.chunks = append([][]byte{{b}}, d.chunks...)
			d.mu.Unlock()
			format = "deflate"
			if b == 0x1f {
				format = "gzip"
			}
		}
	}
	if err == nil {
		switch format {
		case "gzip":
			var z *gzip.Reader
			if z, err = gzip.NewReader(d); err == nil {
				z.Multistream(false)
				r = z
			}
		case "deflate":
			r, err = zlib.NewReader(d)
		case "brotli":
			r = brotli.NewReader(d)
		default:
			r = flate.NewReader(d)
		}
	}
	if err == nil {
		_, err = io.Copy(d, r)
	}
	d.mu.Lock()
	d.done, d.err = true, err
	d.cond.Broadcast()
	d.mu.Unlock()
}

// Read feeds the decoder.
func (d *Decoder) Read(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for len(d.chunks) == 0 && !d.eof {
		d.starved = true
		d.cond.Broadcast()
		d.cond.Wait()
	}
	if len(d.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, d.chunks[0])
	if d.chunks[0] = d.chunks[0][n:]; len(d.chunks[0]) == 0 {
		d.chunks = d.chunks[1:]
	}
	return n, nil
}

// ReadByte makes the decoder read exactly what it needs (flate, gzip and
// zlib wrap a reader without it in a bufio.Reader, which would swallow
// input after the end of the stream).
func (d *Decoder) ReadByte() (byte, error) {
	var b [1]byte
	if _, err := d.Read(b[:]); err != nil {
		return 0, err
	}
	return b[0], nil
}

// Write collects the decoder's output (the decoder's io.Writer side).
func (d *Decoder) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.out.Write(p)
}

func (d *Decoder) take() []byte {
	out := append([]byte(nil), d.out.Bytes()...)
	d.out.Reset()
	return out
}

// Feed decodes chunk, returning the output so far; an error (with the
// output decoded before it) ends the stream.
func (d *Decoder) Feed(chunk []byte) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(chunk) > 0 {
		d.chunks = append(d.chunks, append([]byte(nil), chunk...))
		d.starved = false
		d.cond.Broadcast()
	}
	for !d.starved && !d.done {
		d.cond.Wait()
	}
	switch {
	case d.done && d.err != nil:
		return d.take(), d.err
	case d.done && len(d.chunks) > 0:
		return d.take(), errAfterEnd
	}
	return d.take(), nil
}

var errAfterEnd = errors.New("data after the end of the compressed stream")

// Close ends the input, returning the rest.
func (d *Decoder) Close() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.eof = true
	d.cond.Broadcast()
	for !d.done {
		d.cond.Wait()
	}
	switch {
	case d.err == io.ErrUnexpectedEOF || d.err == io.EOF:
		return nil, io.ErrUnexpectedEOF
	case d.err != nil:
		return nil, d.err
	case len(d.chunks) > 0:
		return nil, errAfterEnd
	}
	return d.take(), nil
}

// Stop ends a decoder whose stream was abandoned.
func (d *Decoder) Stop() {
	d.mu.Lock()
	d.eof = true
	d.cond.Broadcast()
	d.mu.Unlock()
}
