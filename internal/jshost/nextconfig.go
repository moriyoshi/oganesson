package jshost

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

var nextConfigNames = []string{"next.config.ts", "next.config.mts", "next.config.mjs", "next.config.js", "next.config.cjs"}

// FindNextConfig returns the project's next.config.* ("" if none).
func FindNextConfig(dir string) string {
	for _, n := range nextConfigNames {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// PhaseProductionBuild is the phase Next passes to function-style configs.
const PhaseProductionBuild = "phase-production-build"

// NextSetup is what `next build` derives before compiling.
type NextSetup struct {
	// Config is the fully normalized config from Next's own loadConfig.
	Config map[string]any
	// EdgeDefines and ClientDefines come from Next's getDefineEnv.
	EdgeDefines, ClientDefines map[string]string
	// Env holds variables loaded from .env* files.
	Env map[string]string
}

// LoadNext evaluates next.config.* and runs Next's loadConfig (env file
// loading, defaults, validation, finalization) and getDefineEnv, all inside
// one Goja engine so .env values reach the defines exactly as in next build.
func (h *Host) LoadNext(ctx context.Context) (*NextSetup, error) {
	userFile := FindNextConfig(h.Dir)
	userImport, userValue := "", "{}"
	if userFile != "" {
		userImport = fmt.Sprintf("import * as user from %s;", jsonString(userFile))
		userValue = `("default" in user ? user.default : user)`
	}
	entry := fmt.Sprintf(`
import loadConfig from "next/dist/server/config";
import { defaultConfig } from "next/dist/server/config-shared";
import { getDefineEnv } from "next/dist/build/define-env";
%s
globalThis.__ogNextSetup = (async () => {
  const before = { ...process.env };
  let c = %s;
  if (c && typeof c === "object" && Object.keys(c).length === 1 && "default" in c) c = c.default;
  if (typeof c === "function") c = await c(%q, { defaultConfig });
  c = (await c) ?? {};
  // Skip loadWebpackHook (it patches Node's module loader).
  process.env.__NEXT_PRIVATE_RENDER_WORKER = "1";
  const config = await loadConfig(%q, %s, { customConfig: c, silent: true });
  delete process.env.__NEXT_PRIVATE_RENDER_WORKER;
  const env = {};
  for (const k of Object.keys(process.env)) if (before[k] !== process.env[k]) env[k] = process.env[k];
  const common = {
    // og's bundles are webpack-shaped whichever bundler's configuration
    // applies: Turbopack's defines would select its runtime's code paths
    // (__turbopack_clear_chunk_cache__).
    isTurbopack: false, config, dev: false, distDir: config.distDir, projectPath: %s,
    fetchCacheKeyPrefix: "", hasRewrites: false, isNodeServer: false, middlewareMatchers: undefined,
    omitNonDeterministic: false, rewrites: undefined, clientRouterFilters: undefined,
  };
  return {
    config,
    edge: getDefineEnv({ ...common, isClient: false, isEdgeServer: true }),
    client: getDefineEnv({ ...common, isClient: true, isEdgeServer: false }),
    env,
  };
})();
`, userImport, userValue, PhaseProductionBuild, PhaseProductionBuild, jsonString(h.Dir), jsonString(h.Dir))
	// Next resolves its optional OpenTelemetry peer to a compiled copy.
	saved := h.Alias
	h.Alias = map[string]string{"@opentelemetry/api": "next/dist/compiled/@opentelemetry/api"}
	for k, v := range saved {
		h.Alias[k] = v
	}
	// next.config may import modules that await at their top level (nextra's
	// withNextra reaches nextra/dist/{client/pages,server/loader}.js, both
	// top-level await). Enable the ESM top-level-await fallback so Bundle
	// lowers them instead of failing the Goja target ("esnext" + overrides).
	savedTLA := h.TopLevelAwait
	h.TopLevelAwait = true
	code, err := h.Bundle("", entry, "")
	h.TopLevelAwait = savedTLA
	h.Alias = saved
	if err != nil {
		return nil, err
	}
	v, err := h.Eval(ctx, code, "__ogNextSetup")
	if err != nil {
		name := "next.config"
		if userFile != "" {
			name = filepath.Base(userFile)
		}
		return nil, fmt.Errorf("jshost: loading %s: %w", name, err)
	}
	m, _ := v.(map[string]any)
	s := &NextSetup{Env: map[string]string{}}
	s.Config, _ = m["config"].(map[string]any)
	if s.Config == nil {
		return nil, fmt.Errorf("jshost: loadConfig returned no config")
	}
	if s.EdgeDefines, err = stringMap(m["edge"]); err != nil {
		return nil, err
	}
	if s.ClientDefines, err = stringMap(m["client"]); err != nil {
		return nil, err
	}
	if env, ok := m["env"].(map[string]any); ok {
		for k, v := range env {
			s.Env[k] = fmt.Sprint(v)
		}
	}
	return s, nil
}

func stringMap(v any) (map[string]string, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("jshost: expected an object, got %T", v)
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("jshost: define %s is %T, want string", k, val)
		}
		out[k] = s
	}
	return out, nil
}
