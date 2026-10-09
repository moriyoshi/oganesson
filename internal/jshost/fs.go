package jshost

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/grafana/sobek"
)

// fileReads records absolute paths that build-time JS reads through og.fs, so
// the license collector can attribute node_modules assets (CSS, fonts) whose
// bytes a plugin inlined into the output but esbuild never saw as bundle
// inputs (Tailwind's preflight @import "tailwindcss", a package's own
// next/font/local files).
type fileReads struct {
	mu  sync.Mutex
	set map[string]struct{}
}

func newFileReads() *fileReads { return &fileReads{set: map[string]struct{}{}} }

func (r *fileReads) add(p string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.set[p] = struct{}{}
	r.mu.Unlock()
}

// shipAssetExt are the extensions of files whose bytes can end up in the
// shipped output when a build-time plugin reads them (CSS inlined by PostCSS,
// fonts emitted by next/font). Build-time-only JS the host reads (postcss,
// the tailwind plugin) is deliberately excluded: its code is not distributed.
var shipAssetExt = map[string]bool{
	".css": true, ".scss": true, ".sass": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
}

// assets returns the recorded node_modules files whose bytes can ship,
// deduplicated and sorted.
func (r *fileReads) assets() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for p := range r.set {
		if !strings.Contains(filepath.ToSlash(p), "/node_modules/") {
			continue
		}
		if shipAssetExt[strings.ToLower(filepath.Ext(p))] {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// installFS exposes the real filesystem to build-time JS as
// globalThis[Symbol.for("og.fs")], consumed by nodeshim's fs.js. Reads
// are unrestricted; writes are confined to writeRoot (the project). Errors
// come back as {code, message} records; fs.js throws Node-style errors.
// Relative paths are relative to writeRoot, which build-time JS sees as
// process.cwd() (not og's own working directory).
func installFS(vm *sobek.Runtime, writeRoot string, rec *fileReads) error {
	o := vm.NewObject()
	cwdPath := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(writeRoot, p)
	}
	errRec := func(err error, syscallName, path string) sobek.Value {
		code := "EIO"
		switch {
		case errors.Is(err, fs.ErrNotExist):
			code = "ENOENT"
		case errors.Is(err, fs.ErrPermission):
			code = "EACCES"
		case errors.Is(err, syscall.ENOTDIR):
			code = "ENOTDIR"
		case errors.Is(err, syscall.EISDIR):
			code = "EISDIR"
		case errors.Is(err, syscall.EINVAL):
			code = "EINVAL"
		}
		r := vm.NewObject()
		_ = r.Set("error", code)
		_ = r.Set("message", code+": "+err.Error()+", "+syscallName+" '"+path+"'")
		_ = r.Set("syscall", syscallName)
		_ = r.Set("path", path)
		return r
	}
	statRec := func(st os.FileInfo) sobek.Value {
		r := vm.NewObject()
		_ = r.Set("isFile", st.Mode().IsRegular())
		_ = r.Set("isDirectory", st.IsDir())
		_ = r.Set("isSymbolicLink", st.Mode()&os.ModeSymlink != 0)
		_ = r.Set("size", st.Size())
		_ = r.Set("mtimeMs", float64(st.ModTime().UnixNano())/1e6)
		// Exact nanoseconds, as a string (BigInt stats): beyond 2^53.
		_ = r.Set("mtimeNs", strconv.FormatInt(st.ModTime().UnixNano(), 10))
		_ = r.Set("mode", int64(st.Mode().Perm()))
		return r
	}
	_ = o.Set("stat", func(p string, follow bool) sobek.Value {
		p = cwdPath(p)
		var st os.FileInfo
		var err error
		if follow {
			st, err = os.Stat(p)
		} else {
			st, err = os.Lstat(p)
		}
		if err != nil {
			return errRec(err, "stat", p)
		}
		return statRec(st)
	})
	_ = o.Set("readFile", func(p string) sobek.Value {
		p = cwdPath(p)
		b, err := os.ReadFile(p)
		if err != nil {
			return errRec(err, "open", p)
		}
		rec.add(p)
		return vm.ToValue(vm.NewArrayBuffer(b))
	})
	_ = o.Set("readdir", func(p string) sobek.Value {
		p = cwdPath(p)
		entries, err := os.ReadDir(p)
		if err != nil {
			return errRec(err, "scandir", p)
		}
		out := make([]any, 0, len(entries))
		for _, e := range entries {
			r := vm.NewObject()
			_ = r.Set("name", e.Name())
			_ = r.Set("isFile", e.Type().IsRegular())
			_ = r.Set("isDirectory", e.IsDir())
			_ = r.Set("isSymbolicLink", e.Type()&os.ModeSymlink != 0)
			out = append(out, r)
		}
		return vm.ToValue(out)
	})
	_ = o.Set("realpath", func(p string) sobek.Value {
		p = cwdPath(p)
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			return errRec(err, "realpath", p)
		}
		abs, _ := filepath.Abs(r)
		return vm.ToValue(abs)
	})
	_ = o.Set("readlink", func(p string) sobek.Value {
		p = cwdPath(p)
		r, err := os.Readlink(p)
		if err != nil {
			return errRec(err, "readlink", p)
		}
		return vm.ToValue(r)
	})
	root, _ := filepath.Abs(writeRoot)
	writable := func(p string) (string, sobek.Value) {
		abs, err := filepath.Abs(p)
		if err != nil || root == "" || (abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator))) {
			r := vm.NewObject()
			_ = r.Set("error", "EACCES")
			_ = r.Set("message", "EACCES: build-time JavaScript may only write inside the project, write '"+p+"'")
			_ = r.Set("syscall", "open")
			_ = r.Set("path", p)
			return "", r
		}
		return abs, nil
	}
	_ = o.Set("writeFile", func(p string, data sobek.Value, appendTo bool) sobek.Value {
		p = cwdPath(p)
		abs, denied := writable(p)
		if denied != nil {
			return denied
		}
		var b []byte
		switch v := data.Export().(type) {
		case []byte:
			b = v
		case sobek.ArrayBuffer:
			b = v.Bytes()
		default:
			b = []byte(data.String())
		}
		flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		if appendTo {
			flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		}
		f, err := os.OpenFile(abs, flags, 0o644)
		if err == nil {
			_, err = f.Write(b)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			return errRec(err, "open", p)
		}
		return sobek.Undefined()
	})
	_ = o.Set("mkdir", func(p string, recursive bool) sobek.Value {
		p = cwdPath(p)
		abs, denied := writable(p)
		if denied != nil {
			return denied
		}
		var err error
		if recursive {
			err = os.MkdirAll(abs, 0o755)
		} else {
			err = os.Mkdir(abs, 0o755)
		}
		if err != nil {
			return errRec(err, "mkdir", p)
		}
		return sobek.Undefined()
	})
	_ = o.Set("rm", func(p string, recursive bool) sobek.Value {
		p = cwdPath(p)
		abs, denied := writable(p)
		if denied != nil {
			return denied
		}
		var err error
		if recursive {
			err = os.RemoveAll(abs)
		} else {
			err = os.Remove(abs)
		}
		if err != nil {
			return errRec(err, "rm", p)
		}
		return sobek.Undefined()
	})
	_ = o.Set("rename", func(from, to string) sobek.Value {
		from = cwdPath(from)
		to = cwdPath(to)
		a, denied := writable(from)
		if denied != nil {
			return denied
		}
		b, denied := writable(to)
		if denied != nil {
			return denied
		}
		if err := os.Rename(a, b); err != nil {
			return errRec(err, "rename", from)
		}
		return sobek.Undefined()
	})
	_ = o.Set("chmod", func(p string, mode int64) sobek.Value {
		p = cwdPath(p)
		abs, denied := writable(p)
		if denied != nil {
			return denied
		}
		if err := os.Chmod(abs, os.FileMode(mode)&os.ModePerm); err != nil {
			return errRec(err, "chmod", p)
		}
		return sobek.Undefined()
	})
	install, err := vm.RunString(`(function (o) { Object.defineProperty(globalThis, Symbol.for("og.fs"), { value: o }); })`)
	if err != nil {
		return err
	}
	fn, _ := sobek.AssertFunction(install)
	_, err = fn(sobek.Undefined(), o)
	return err
}
