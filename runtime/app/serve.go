package app

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

// Serve serves the application in fsys on addr until ctx is done, then
// shuts down gracefully. wrap, if not nil, wraps the application's handler
// (e.g. extra routes or middleware in an ejected project).
func Serve(ctx context.Context, fsys fs.FS, addr string, wrap func(http.Handler) http.Handler) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return serve(ctx, fsys, ln, wrap)
}

// serve is Serve on a listener, which it closes.
func serve(ctx context.Context, fsys fs.FS, ln net.Listener, wrap func(http.Handler) http.Handler) error {
	// next start's start-server sets this once it listens; Next fetches a
	// server action's redirect target from it (action-handler.js). Set
	// before Handler, which snapshots the environment.
	if _, ok := os.LookupEnv(PrivateOriginEnv); !ok {
		os.Setenv(PrivateOriginEnv, privateOrigin(ln.Addr()))
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	h, meta, err := Handler(fsys, logger)
	if err != nil {
		ln.Close()
		return err
	}
	if meta.Close != nil {
		defer meta.Close()
	}
	if wrap != nil {
		h = wrap(h)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 30 * time.Second}
	fmt.Fprintf(os.Stderr, "og: serving %s app (build %s) on http://%s\n", meta.Framework, meta.BuildID, ln.Addr())
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// PrivateOriginEnv is the server's own origin, as next start sets it.
const PrivateOriginEnv = "__NEXT_PRIVATE_ORIGIN"

// privateOrigin is start-server's appUrl for a listener: http://host:port,
// with localhost for an unspecified address and an IPv6 host bracketed.
func privateOrigin(a net.Addr) string {
	host, port := "localhost", ""
	if t, ok := a.(*net.TCPAddr); ok {
		port = strconv.Itoa(t.Port)
		if !t.IP.IsUnspecified() {
			host = t.IP.String()
		}
	}
	return "http://" + net.JoinHostPort(host, port)
}

// DefaultAddr is the listen address: ":$PORT", or ":3000".
func DefaultAddr() string {
	if p := os.Getenv("PORT"); p != "" {
		return ":" + p
	}
	return ":3000"
}
