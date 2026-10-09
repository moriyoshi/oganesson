package vfs

import (
	"bytes"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Overlay is the runtime's filesystem: the files packaged with the app
// (read-only, below) under an in-memory layer that takes every write. It is
// process-wide (requests' VMs share it, as they share og:kv) and lost when
// the process exits. Names are slash-separated and relative to the root
// ("." for the root itself), as in io/fs.
//
// A name can also be backed by a provider instead of bytes (a SQLite
// database the natives keep in memory): reads ask it for the current
// content, writes hand it the new content.
type Overlay struct {
	mu      sync.RWMutex
	lower   fs.FS
	upper   map[string]*node // written files and created directories
	gone    map[string]bool  // lower-layer names removed (whiteouts)
	special map[string]Provider
}

// Provider backs a name with live content.
type Provider interface {
	Content() ([]byte, error)
	Replace(data []byte) error
}

type node struct {
	dir   bool
	data  []byte
	mode  fs.FileMode
	mtime time.Time
}

// NewOverlay layers a writable, in-memory filesystem over lower.
func NewOverlay(lower fs.FS) *Overlay {
	return &Overlay{lower: lower, upper: map[string]*node{}, gone: map[string]bool{}, special: map[string]Provider{}}
}

func clean(name string) string {
	if c := path.Clean("/" + name); c != "/" {
		return c[1:]
	}
	return "."
}

func pathErr(op, name string, err error) error { return &fs.PathError{Op: op, Path: name, Err: err} }

type info struct {
	name  string
	size  int64
	mode  fs.FileMode
	mtime time.Time
}

func (i info) Name() string       { return path.Base(i.name) }
func (i info) Size() int64        { return i.size }
func (i info) Mode() fs.FileMode  { return i.mode }
func (i info) ModTime() time.Time { return i.mtime }
func (i info) IsDir() bool        { return i.mode.IsDir() }
func (i info) Sys() any           { return nil }

// hidden reports a lower-layer name removed, itself or an ancestor.
func (o *Overlay) hidden(name string) bool {
	for n := name; n != "."; n = path.Dir(n) {
		if o.gone[n] {
			return true
		}
	}
	return false
}

// stat is Stat with o.mu held.
func (o *Overlay) stat(name string) (fs.FileInfo, error) {
	if n, ok := o.upper[name]; ok {
		mode, size := n.mode, int64(len(n.data))
		if n.dir {
			mode |= fs.ModeDir
		}
		return info{name, size, mode, n.mtime}, nil
	}
	if p, ok := o.special[name]; ok {
		b, err := p.Content()
		if err != nil {
			return nil, pathErr("stat", name, err)
		}
		return info{name, int64(len(b)), 0o644, time.Now()}, nil
	}
	if name != "." && o.hidden(name) {
		return nil, pathErr("stat", name, fs.ErrNotExist)
	}
	st, err := fs.Stat(o.lower, name)
	if err != nil {
		return nil, err
	}
	mode := fs.FileMode(0o644)
	if st.IsDir() {
		mode = fs.ModeDir | 0o755
	}
	return info{name, st.Size(), mode, st.ModTime()}, nil
}

// Stat describes name.
func (o *Overlay) Stat(name string) (fs.FileInfo, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.stat(clean(name))
}

// ReadFile reads name.
func (o *Overlay) ReadFile(name string) ([]byte, error) {
	name = clean(name)
	o.mu.RLock()
	defer o.mu.RUnlock()
	if n, ok := o.upper[name]; ok {
		if n.dir {
			return nil, pathErr("read", name, syscall.EISDIR)
		}
		return append([]byte(nil), n.data...), nil
	}
	if p, ok := o.special[name]; ok {
		return p.Content()
	}
	if o.hidden(name) {
		return nil, pathErr("open", name, fs.ErrNotExist)
	}
	if st, err := fs.Stat(o.lower, name); err == nil && st.IsDir() {
		return nil, pathErr("read", name, syscall.EISDIR)
	}
	return fs.ReadFile(o.lower, name)
}

type dirEntry struct{ fs.FileInfo }

func (d dirEntry) Type() fs.FileMode          { return d.Mode().Type() }
func (d dirEntry) Info() (fs.FileInfo, error) { return d.FileInfo, nil }

// ReadDir lists name, both layers merged, sorted by name.
func (o *Overlay) ReadDir(name string) ([]fs.DirEntry, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.readDirLocked(clean(name))
}

func (o *Overlay) readDirLocked(name string) ([]fs.DirEntry, error) {
	st, err := o.stat(name)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, pathErr("readdir", name, syscall.ENOTDIR)
	}
	seen := map[string]fs.DirEntry{}
	if name == "." || !o.hidden(name) {
		if lower, err := fs.ReadDir(o.lower, name); err == nil {
			for _, e := range lower {
				child := path.Join(name, e.Name())
				if o.gone[child] {
					continue
				}
				if st, err := o.stat(child); err == nil {
					seen[e.Name()] = dirEntry{st}
				}
			}
		}
	}
	prefix := name + "/"
	if name == "." {
		prefix = ""
	}
	for k := range o.upper {
		if rest, ok := strings.CutPrefix(k, prefix); ok && rest != "" && !strings.Contains(rest, "/") {
			if st, err := o.stat(k); err == nil {
				seen[rest] = dirEntry{st}
			}
		}
	}
	for k := range o.special {
		if rest, ok := strings.CutPrefix(k, prefix); ok && rest != "" && !strings.Contains(rest, "/") {
			if st, err := o.stat(k); err == nil {
				seen[rest] = dirEntry{st}
			}
		}
	}
	out := make([]fs.DirEntry, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

// parentDir checks that name's parent is a directory (o.mu held).
func (o *Overlay) parentDir(op, name string) error {
	parent := path.Dir(name)
	st, err := o.stat(parent)
	if err != nil {
		return pathErr(op, name, fs.ErrNotExist)
	}
	if !st.IsDir() {
		return pathErr(op, name, syscall.ENOTDIR)
	}
	return nil
}

// WriteFile writes data to name (appends with appendTo), creating it.
func (o *Overlay) WriteFile(name string, data []byte, appendTo bool) error {
	name = clean(name)
	o.mu.Lock()
	defer o.mu.Unlock()
	if p, ok := o.special[name]; ok {
		if appendTo {
			old, err := p.Content()
			if err != nil {
				return err
			}
			data = append(old, data...)
		}
		return p.Replace(append([]byte(nil), data...))
	}
	if err := o.parentDir("open", name); err != nil {
		return err
	}
	st, err := o.stat(name)
	if err == nil && st.IsDir() {
		return pathErr("open", name, syscall.EISDIR)
	}
	var buf []byte
	mode := fs.FileMode(0o644)
	if appendTo && err == nil {
		old, err := o.readLocked(name)
		if err != nil {
			return err
		}
		buf = append(old, data...)
		mode = st.Mode().Perm()
	} else {
		buf = append([]byte(nil), data...)
	}
	o.upper[name] = &node{data: buf, mode: mode, mtime: time.Now()}
	return nil
}

func (o *Overlay) readLocked(name string) ([]byte, error) {
	if n, ok := o.upper[name]; ok {
		return append([]byte(nil), n.data...), nil
	}
	return fs.ReadFile(o.lower, name)
}

// Mkdir creates name (and its parents with all).
func (o *Overlay) Mkdir(name string, all bool) error {
	name = clean(name)
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.mkdir(name, all)
}

func (o *Overlay) mkdir(name string, all bool) error {
	if name == "." {
		if all {
			return nil
		}
		return pathErr("mkdir", name, fs.ErrExist)
	}
	if st, err := o.stat(name); err == nil {
		if all && st.IsDir() {
			return nil
		}
		return pathErr("mkdir", name, fs.ErrExist)
	}
	if all {
		if err := o.mkdir(path.Dir(name), true); err != nil {
			return err
		}
	} else if err := o.parentDir("mkdir", name); err != nil {
		return err
	}
	o.upper[name] = &node{dir: true, mode: 0o755, mtime: time.Now()}
	return nil
}

// Remove removes name; a directory only if empty, unless all.
func (o *Overlay) Remove(name string, all bool) error {
	name = clean(name)
	o.mu.Lock()
	defer o.mu.Unlock()
	if name == "." {
		return pathErr("rm", name, syscall.EBUSY)
	}
	st, err := o.stat(name)
	if err != nil {
		return err
	}
	if st.IsDir() && !all {
		if entries, _ := o.readDirLocked(name); len(entries) > 0 {
			return pathErr("rmdir", name, syscall.ENOTEMPTY)
		}
	}
	prefix := name + "/"
	for k := range o.upper {
		if k == name || strings.HasPrefix(k, prefix) {
			delete(o.upper, k)
		}
	}
	for k := range o.special {
		if k == name || strings.HasPrefix(k, prefix) {
			delete(o.special, k)
		}
	}
	if _, err := fs.Stat(o.lower, name); err == nil {
		o.gone[name] = true
	}
	return nil
}

// Rename moves from to to (a directory with everything in it).
func (o *Overlay) Rename(from, to string) error {
	from, to = clean(from), clean(to)
	o.mu.Lock()
	st, err := o.stat(from)
	if err != nil {
		o.mu.Unlock()
		return err
	}
	if err := o.parentDir("rename", to); err != nil {
		o.mu.Unlock()
		return err
	}
	if _, special := o.special[from]; special {
		o.mu.Unlock()
		return pathErr("rename", from, syscall.EBUSY)
	}
	o.mu.Unlock()
	if st.IsDir() {
		if err := o.copyTree(from, to); err != nil {
			return err
		}
	} else {
		data, err := o.ReadFile(from)
		if err != nil {
			return err
		}
		if err := o.WriteFile(to, data, false); err != nil {
			return err
		}
	}
	return o.Remove(from, true)
}

func (o *Overlay) copyTree(from, to string) error {
	if err := o.Mkdir(to, true); err != nil {
		return err
	}
	entries, err := o.ReadDir(from)
	if err != nil {
		return err
	}
	for _, e := range entries {
		src, dst := path.Join(from, e.Name()), path.Join(to, e.Name())
		if e.IsDir() {
			if err := o.copyTree(src, dst); err != nil {
				return err
			}
			continue
		}
		data, err := o.ReadFile(src)
		if err != nil {
			return err
		}
		if err := o.WriteFile(dst, data, false); err != nil {
			return err
		}
	}
	return nil
}

// Chmod sets name's permission bits.
func (o *Overlay) Chmod(name string, mode fs.FileMode) error {
	name = clean(name)
	o.mu.Lock()
	defer o.mu.Unlock()
	st, err := o.stat(name)
	if err != nil {
		return err
	}
	n, ok := o.upper[name]
	if !ok {
		data := []byte(nil)
		if !st.IsDir() {
			if data, err = o.readLocked(name); err != nil {
				return err
			}
		}
		n = &node{dir: st.IsDir(), data: data, mtime: st.ModTime()}
		o.upper[name] = n
	}
	n.mode = mode.Perm()
	return nil
}

// Provide backs name with p (creating its parent directories), or, with
// p nil, ends that. The name's current bytes, if any, are the provider's
// to take first (Initial).
func (o *Overlay) Provide(name string, p Provider) error {
	name = clean(name)
	o.mu.Lock()
	defer o.mu.Unlock()
	if p == nil {
		delete(o.special, name)
		return nil
	}
	if err := o.mkdir(path.Dir(name), true); err != nil {
		return err
	}
	delete(o.upper, name)
	o.special[name] = p
	return nil
}

// ProviderOf is the provider backing name, nil if none.
func (o *Overlay) ProviderOf(name string) Provider {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.special[clean(name)]
}

// Initial is name's content before a provider takes it over: nil, false
// when there is none.
func (o *Overlay) Initial(name string) ([]byte, bool) {
	name = clean(name)
	o.mu.RLock()
	defer o.mu.RUnlock()
	if p, ok := o.special[name]; ok {
		b, err := p.Content()
		return b, err == nil
	}
	if st, err := o.stat(name); err != nil || st.IsDir() {
		return nil, false
	}
	b, err := o.readLocked(name)
	return b, err == nil
}

// Open opens name for reading (fs.FS).
func (o *Overlay) Open(name string) (fs.File, error) {
	st, err := o.Stat(name)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		entries, err := o.ReadDir(name)
		if err != nil {
			return nil, err
		}
		return &openDir{info: st, entries: entries}, nil
	}
	data, err := o.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return &openFile{info: st, Reader: bytes.NewReader(data)}, nil
}

type openFile struct {
	info fs.FileInfo
	*bytes.Reader
}

func (f *openFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *openFile) Close() error               { return nil }

type openDir struct {
	info    fs.FileInfo
	entries []fs.DirEntry
}

func (d *openDir) Stat() (fs.FileInfo, error) { return d.info, nil }
func (d *openDir) Read([]byte) (int, error)   { return 0, pathErr("read", d.info.Name(), syscall.EISDIR) }
func (d *openDir) Close() error               { return nil }
func (d *openDir) ReadDir(n int) ([]fs.DirEntry, error) {
	if n <= 0 {
		out := d.entries
		d.entries = nil
		return out, nil
	}
	if len(d.entries) == 0 {
		return nil, io.EOF
	}
	if n > len(d.entries) {
		n = len(d.entries)
	}
	out := d.entries[:n]
	d.entries = d.entries[n:]
	return out, nil
}

// Empty is a filesystem with nothing in it but its root.
var Empty fs.FS = emptyFS{}

type emptyFS struct{}

func (emptyFS) Open(name string) (fs.File, error) {
	if name == "." {
		return &openDir{info: info{".", 0, fs.ModeDir | 0o755, time.Time{}}}, nil
	}
	return nil, pathErr("open", name, fs.ErrNotExist)
}
