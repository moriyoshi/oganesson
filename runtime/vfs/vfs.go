// Package vfs gives a request's JavaScript a read-only filesystem: files
// packaged with the app, mounted at "/" (the runtime's process.cwd()). It
// provides globalThis[Symbol.for("og.fs")], the interface runtime/nodeshim's
// fs.js uses (as internal/jshost does at build time with the real one).
package vfs

import (
	"errors"
	"io/fs"
	"path"
	"strconv"
	"syscall"

	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/runtime/engine"
)

// Install exposes fsys, mounted at "/", to e's JavaScript.
func Install(e *engine.Engine, fsys fs.FS) error {
	vm := e.VM
	o := vm.NewObject()
	rec := func(code, msg, syscall, p string) sobek.Value {
		r := vm.NewObject()
		_ = r.Set("error", code)
		_ = r.Set("message", code+": "+msg+", "+syscall+" '"+p+"'")
		_ = r.Set("syscall", syscall)
		_ = r.Set("path", p)
		return r
	}
	errRec := func(err error, op, p string) sobek.Value {
		// The errnos first: syscall.Errno matches fs.ErrExist for
		// ENOTEMPTY too.
		code := "EIO"
		switch {
		case errors.Is(err, syscall.ENOTEMPTY):
			code = "ENOTEMPTY"
		case errors.Is(err, syscall.ENOTDIR):
			code = "ENOTDIR"
		case errors.Is(err, syscall.EISDIR):
			code = "EISDIR"
		case errors.Is(err, syscall.EBUSY):
			code = "EBUSY"
		case errors.Is(err, fs.ErrNotExist):
			code = "ENOENT"
		case errors.Is(err, fs.ErrPermission):
			code = "EACCES"
		case errors.Is(err, fs.ErrExist):
			code = "EEXIST"
		}
		return rec(code, err.Error(), op, p)
	}
	// name maps a JS path to its name in fsys ("." for the root).
	name := func(p string) string {
		if c := path.Clean("/" + p); c != "/" {
			return c[1:]
		}
		return "."
	}
	statRec := func(st fs.FileInfo) sobek.Value {
		r := vm.NewObject()
		_ = r.Set("isFile", st.Mode().IsRegular())
		_ = r.Set("isDirectory", st.IsDir())
		_ = r.Set("isSymbolicLink", false)
		_ = r.Set("size", st.Size())
		_ = r.Set("mtimeMs", float64(st.ModTime().UnixNano())/1e6)
		_ = r.Set("mtimeNs", strconv.FormatInt(st.ModTime().UnixNano(), 10))
		mode := int64(0o444)
		if st.IsDir() {
			mode = 0o555
		}
		if _, ok := fsys.(*Overlay); ok {
			mode = int64(st.Mode().Perm())
		}
		_ = r.Set("mode", mode)
		return r
	}
	_ = o.Set("stat", func(p string, follow bool) sobek.Value {
		st, err := fs.Stat(fsys, name(p))
		if err != nil {
			return errRec(err, "stat", p)
		}
		return statRec(st)
	})
	_ = o.Set("readFile", func(p string) sobek.Value {
		b, err := fs.ReadFile(fsys, name(p))
		if err != nil {
			return errRec(err, "open", p)
		}
		return vm.ToValue(vm.NewArrayBuffer(b))
	})
	_ = o.Set("readdir", func(p string) sobek.Value {
		entries, err := fs.ReadDir(fsys, name(p))
		if err != nil {
			return errRec(err, "scandir", p)
		}
		out := make([]any, 0, len(entries))
		for _, d := range entries {
			r := vm.NewObject()
			_ = r.Set("name", d.Name())
			_ = r.Set("isFile", d.Type().IsRegular())
			_ = r.Set("isDirectory", d.IsDir())
			_ = r.Set("isSymbolicLink", false)
			out = append(out, r)
		}
		return vm.ToValue(out)
	})
	_ = o.Set("realpath", func(p string) sobek.Value {
		if _, err := fs.Stat(fsys, name(p)); err != nil {
			return errRec(err, "realpath", p)
		}
		if n := name(p); n != "." {
			return vm.ToValue("/" + n)
		}
		return vm.ToValue("/")
	})
	if ov, ok := fsys.(*Overlay); ok {
		installWrites(vm, o, ov, name, errRec)
	} else {
		readOnly := func(syscall string) func(p string) sobek.Value {
			return func(p string) sobek.Value { return rec("EROFS", "read-only file system", syscall, p) }
		}
		for _, op := range []string{"writeFile", "mkdir", "rm", "rename", "chmod"} {
			_ = o.Set(op, readOnly(op))
		}
	}
	install, err := vm.RunString(`(o) => Object.defineProperty(globalThis, Symbol.for("og.fs"), { value: o })`)
	if err != nil {
		return err
	}
	fn, _ := sobek.AssertFunction(install)
	_, err = fn(sobek.Undefined(), o)
	return err
}

// installWrites gives og.fs the overlay's write operations (the build
// host's interface: writeFile(path, bytes, append), mkdir(path, recursive),
// rm(path, recursive), rename(from, to), chmod(path, mode)).
func installWrites(vm *sobek.Runtime, o *sobek.Object, ov *Overlay, name func(string) string, errRec func(error, string, string) sobek.Value) {
	ok := sobek.Undefined()
	_ = o.Set("writeFile", func(p string, data sobek.Value, appendTo bool) sobek.Value {
		var b []byte
		switch v := data.Export().(type) {
		case sobek.ArrayBuffer:
			b = v.Bytes()
		case []byte:
			b = v
		case string:
			b = []byte(v)
		default:
			if err := vm.ExportTo(data, &b); err != nil {
				return errRec(err, "open", p)
			}
		}
		if err := ov.WriteFile(name(p), b, appendTo); err != nil {
			return errRec(err, "open", p)
		}
		return ok
	})
	_ = o.Set("mkdir", func(p string, recursive bool) sobek.Value {
		if err := ov.Mkdir(name(p), recursive); err != nil {
			return errRec(err, "mkdir", p)
		}
		return ok
	})
	_ = o.Set("rm", func(p string, recursive bool) sobek.Value {
		if err := ov.Remove(name(p), recursive); err != nil {
			return errRec(err, "rm", p)
		}
		return ok
	})
	_ = o.Set("rename", func(from, to string) sobek.Value {
		if err := ov.Rename(name(from), name(to)); err != nil {
			return errRec(err, "rename", from)
		}
		return ok
	})
	_ = o.Set("chmod", func(p string, mode int64) sobek.Value {
		if err := ov.Chmod(name(p), fs.FileMode(mode)); err != nil {
			return errRec(err, "chmod", p)
		}
		return ok
	})
}
