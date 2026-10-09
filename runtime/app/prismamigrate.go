package app

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/runtime/engine"
	"github.com/moriyoshi/oganesson/runtime/webapi"
)

// runPrismaMigrate applies the project's Prisma migrations once, at app startup,
// by running the bundled migrator program (packaged into Meta.Files) in a
// one-off engine that shares the application's native services and filesystem
// overlay. files is the Files-rooted FS; exts and webAPI are the same host APIs
// the request engines get, so the migrator drives the same database the server
// then reads. A migration failure fails startup.
func runPrismaMigrate(files fs.FS, meta *Meta, webAPI webapi.Options, exts []func(*engine.Engine) error, env map[string]string, logger *slog.Logger) error {
	pm := meta.PrismaMigrate
	code, err := fs.ReadFile(files, pm.Program)
	if err != nil {
		return fmt.Errorf("reading migrator program %s: %w", pm.Program, err)
	}
	url := pm.URL
	if url == "" && pm.URLEnv != "" {
		url = env[pm.URLEnv]
	}
	if url == "" {
		return fmt.Errorf("no datasource connection URL (provider %s): set %s", pm.Provider, pm.URLEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// Disable the Prisma schema engine's advisory lock. It otherwise holds a
	// pg_advisory_lock on one connection while applying migrations on another,
	// so migrate needs two concurrent connections — which a single-session
	// server (PGlite) cannot grant. The one-off startup migrator is the only
	// writer, so the lock is unnecessary here. Harmless for sqlite.
	migEnv := map[string]string{}
	for k, v := range webAPI.Env {
		migEnv[k] = v
	}
	migEnv["PRISMA_SCHEMA_DISABLE_ADVISORY_LOCK"] = "false"
	migWebAPI := webAPI
	migWebAPI.Env = migEnv

	var uncaught error
	e := engine.New(engine.Options{Uncaught: func(err error) error { uncaught = err; return nil }})
	defer e.Close()
	if _, err := webapi.Install(e, migWebAPI); err != nil {
		return err
	}
	for _, ext := range exts {
		if err := ext(e); err != nil {
			return err
		}
	}

	var done sobek.Value
	err = e.Run(ctx, func(vm *sobek.Runtime) error {
		cfg := vm.NewObject()
		_ = cfg.Set("dir", pm.Dir)
		_ = cfg.Set("url", url)
		_ = cfg.Set("wasm", pm.Wasm)
		_ = cfg.Set("push", pm.Push)
		_ = cfg.Set("schema", pm.Schema)
		_ = cfg.Set("acceptDataLoss", pm.AcceptDataLoss)
		if err := vm.GlobalObject().Set("__ogPrismaMigrate", cfg); err != nil {
			return err
		}
		if _, err := vm.RunScript("og-prisma-migrate.js", string(code)); err != nil {
			return err
		}
		done = vm.GlobalObject().Get("__ogMigrateDone")
		return nil
	})
	if err != nil {
		return err
	}
	if done == nil || sobek.IsUndefined(done) {
		return fmt.Errorf("migrator program did not start")
	}
	result, err := e.Await(ctx, done)
	if err != nil {
		return err
	}
	if uncaught != nil {
		return uncaught
	}
	// result is { applied: [...] } for deploy or { push, executedSteps, … } for
	// db push; log whichever the migrator returned.
	var detail any
	if result != nil {
		detail = result.Export()
	}
	if pm.Push {
		logger.Info("prisma db push", "provider", pm.Provider, "result", detail)
	} else {
		logger.Info("prisma migrate deploy", "provider", pm.Provider, "result", detail)
	}
	return nil
}
