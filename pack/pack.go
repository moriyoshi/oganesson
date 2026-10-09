// Package pack appends an application payload (a zip archive) to an
// executable, and finds it again at startup. The payload is followed by a
// fixed trailer: the zip's length (8 bytes, little endian) and a magic tag.
//
//	[executable][zip][len uint64][magic "OGPAYLD1"]
package pack

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
)

const magic = "OGPAYLD1"

const trailerSize = 8 + len(magic)

// ErrNoPayload reports an executable without an appended payload.
var ErrNoPayload = errors.New("pack: no payload")

// Write copies the executable at exePath to out, without any payload it may
// already carry, and appends files as the new payload.
func Write(out io.Writer, exePath string, files map[string][]byte) error {
	exe, err := os.Open(exePath)
	if err != nil {
		return err
	}
	defer exe.Close()
	size, err := baseSize(exe)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.NewSectionReader(exe, 0, size)); err != nil {
		return err
	}
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Deflate})
		if err != nil {
			return err
		}
		if _, err := w.Write(files[n]); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if _, err := out.Write(zbuf.Bytes()); err != nil {
		return err
	}
	var trailer [trailerSize]byte
	binary.LittleEndian.PutUint64(trailer[:8], uint64(zbuf.Len()))
	copy(trailer[8:], magic)
	_, err = out.Write(trailer[:])
	return err
}

// Open returns the payload appended to the executable at exePath.
func Open(exePath string) (fs.FS, error) {
	f, err := os.Open(exePath)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	zipLen, ok, err := readTrailer(f, st.Size())
	if err != nil || !ok {
		f.Close()
		if err == nil {
			err = ErrNoPayload
		}
		return nil, err
	}
	start := st.Size() - int64(trailerSize) - zipLen
	zr, err := zip.NewReader(io.NewSectionReader(f, start, zipLen), zipLen)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("pack: corrupt payload: %w", err)
	}
	// The file stays open for the life of the process (the payload is
	// served from it).
	return zr, nil
}

// baseSize is the executable's size without an existing payload.
func baseSize(f *os.File) (int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	zipLen, ok, err := readTrailer(f, st.Size())
	if err != nil {
		return 0, err
	}
	if !ok {
		return st.Size(), nil
	}
	return st.Size() - int64(trailerSize) - zipLen, nil
}

func readTrailer(r io.ReaderAt, size int64) (int64, bool, error) {
	if size < int64(trailerSize) {
		return 0, false, nil
	}
	var t [trailerSize]byte
	if _, err := r.ReadAt(t[:], size-int64(trailerSize)); err != nil {
		return 0, false, err
	}
	if string(t[8:]) != magic {
		return 0, false, nil
	}
	n := int64(binary.LittleEndian.Uint64(t[:8]))
	if n <= 0 || n > size-int64(trailerSize) {
		return 0, false, fmt.Errorf("pack: invalid payload length %d", n)
	}
	return n, true, nil
}
