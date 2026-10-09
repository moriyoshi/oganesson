package vite

import (
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing/fstest"

	"github.com/moriyoshi/oganesson/runtime/app"
)

// previewServer serves a just-built app the way the packaged binary will
// (runtime/app over the client output and the server bundled for Goja),
// on a local port: what `vite preview` is to Vite, for build-time tools
// that render through it (TanStack Start's prerendering and SPA shell).
type previewServer struct {
	ln  net.Listener
	srv *http.Server
}

var (
	previewMu sync.Mutex
	previews  = map[int]*previewServer{}
	previewID int
)

// startPreview bundles serverEntry for Goja and serves it behind the files
// of clientDir; env reaches the server's process.env.
func startPreview(root, clientDir, serverEntry string, env map[string]string) (id int, url string, err error) {
	files := fstest.MapFS{}
	js, err := bundleServer(root, serverEntry, nil) // its output is not shipped
	if err != nil {
		return 0, "", err
	}
	files[app.ServerFile] = &fstest.MapFile{Data: []byte(js)}
	meta := app.Meta{Format: app.FormatVersion, Framework: "vite", AppType: "custom", Env: env}
	files[app.MetaFile] = &fstest.MapFile{Data: []byte(mustJSON(meta))}
	err = filepath.WalkDir(clientDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(clientDir, p)
		files[app.ClientDir+"/"+filepath.ToSlash(rel)] = &fstest.MapFile{Data: b}
		return nil
	})
	if err != nil {
		return 0, "", err
	}
	h, _, err := app.Handler(files, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return 0, "", err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, "", err
	}
	return servePreview(ln, h), "http://" + ln.Addr().String() + "/", nil
}

// startProgramPreview serves the project in root through the server
// program of its scripts, packaged as og packages it (its files read-only
// at "/"), on a local port; the program's PORT is that port.
func startProgramPreview(root string) (id int, url string, err error) {
	pkg := readPackage(root)
	prog := serverProgram(root, pkg)
	if prog == nil {
		return 0, "", fmt.Errorf("og: no preview / start / serve script runs a server program in %s", root)
	}
	js, err := bundleProgram(root, prog.File, prog.Args, pkg.DevDependencies, nil) // its output is not shipped
	if err != nil {
		return 0, "", err
	}
	out := &Output{AppType: "custom", Files: map[string][]byte{}, ServerJS: js, Env: map[string]string{}}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out.Files[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		return 0, "", err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, "", err
	}
	for k, v := range prog.Env {
		out.Env[k] = v
	}
	out.Env["PORT"] = strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	files := fstest.MapFS{}
	for name, b := range out.Payload() {
		files[name] = &fstest.MapFile{Data: b}
	}
	h, _, err := app.Handler(files, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		_ = ln.Close()
		return 0, "", err
	}
	return servePreview(ln, h), "http://" + ln.Addr().String() + "/", nil
}

// servePreview serves h on ln until stopPreview.
func servePreview(ln net.Listener, h http.Handler) int {
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	previewMu.Lock()
	defer previewMu.Unlock()
	previewID++
	previews[previewID] = &previewServer{ln: ln, srv: srv}
	return previewID
}

func stopPreview(id int) {
	previewMu.Lock()
	p := previews[id]
	delete(previews, id)
	previewMu.Unlock()
	if p != nil {
		_ = p.srv.Close()
	}
}
