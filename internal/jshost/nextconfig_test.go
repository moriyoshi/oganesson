package jshost

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

const fixture = "../../testdata/apps/next-hello"

func fixtureNodeModules(t *testing.T) string {
	t.Helper()
	dir, _ := filepath.Abs(filepath.Join(fixture, "node_modules"))
	if _, err := os.Stat(filepath.Join(dir, "next")); err != nil {
		t.Skip("run: go run ./cmd/og install testdata/apps/next-hello")
	}
	return dir
}

func project(t *testing.T, files map[string]string) string {
	t.Helper()
	tmp := t.TempDir()
	if err := os.Symlink(fixtureNodeModules(t), filepath.Join(tmp, "node_modules")); err != nil {
		t.Fatal(err)
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return tmp
}

func TestLoadNext(t *testing.T) {
	dir := project(t, map[string]string{
		"next.config.ts": `import type { NextConfig } from "next";
import path from "node:path";
const config: NextConfig = { basePath: "/base", env: { DIR: path.basename(__dirname) } };
export default config;`,
		".env.production": "NEXT_PUBLIC_GREETING=hello\nSECRET=s3cret\n",
	})
	h := &Host{Dir: dir, Out: io.Discard}
	s, err := h.LoadNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	exp, _ := s.Config["experimental"].(map[string]any)
	ii, _ := exp["instantInsights"].(map[string]any)
	if s.Config["basePath"] != "/base" || ii["validationLevel"] != "warning" {
		t.Errorf("config not normalized: basePath=%v instantInsights=%v", s.Config["basePath"], ii)
	}
	if s.Env["SECRET"] != "s3cret" {
		t.Errorf(".env not loaded: %v", s.Env)
	}
	for k, want := range map[string]string{
		"process.env.NEXT_RUNTIME":            `"edge"`,
		"process.env.__NEXT_USE_NODE_STREAMS": "false",
		"process.env.NEXT_PUBLIC_GREETING":    `"hello"`,
		"process.env.DIR":                     `"` + filepath.Base(dir) + `"`,
	} {
		if s.EdgeDefines[k] != want {
			t.Errorf("edge define %s = %q, want %q", k, s.EdgeDefines[k], want)
		}
	}
	if s.ClientDefines["process.env.NEXT_RUNTIME"] != `""` {
		t.Errorf("client NEXT_RUNTIME = %q", s.ClientDefines["process.env.NEXT_RUNTIME"])
	}
}

func TestLoadNextFunctionConfig(t *testing.T) {
	dir := project(t, map[string]string{
		"next.config.mjs": `export default async (phase, { defaultConfig }) => ({ poweredByHeader: phase !== "phase-production-build", trailingSlash: !!defaultConfig });`,
	})
	s, err := (&Host{Dir: dir, Out: io.Discard}).LoadNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Config["poweredByHeader"] != false || s.Config["trailingSlash"] != true {
		t.Errorf("function config not applied: %v %v", s.Config["poweredByHeader"], s.Config["trailingSlash"])
	}
}

// TestLoadNextTopLevelAwait guards the config-load path against modules that
// await at their top level, as nextra's withNextra does (it reaches
// nextra/dist/{client/pages,server/loader}.js, both top-level await). The Goja
// target lists top-level-await among its unsupported features, so without the
// ESM fallback esbuild fails with `Top-level await is not available in the
// configured target environment ("esnext" + N overrides)`.
func TestLoadNextTopLevelAwait(t *testing.T) {
	dir := project(t, map[string]string{
		"tla.mjs": `export const basePath = await Promise.resolve("/tla");`,
		"next.config.mjs": `import { basePath } from "./tla.mjs";
export default { basePath };`,
	})
	s, err := (&Host{Dir: dir, Out: io.Discard}).LoadNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Config["basePath"] != "/tla" {
		t.Errorf("basePath = %v, want /tla (top-level await value lost)", s.Config["basePath"])
	}
}

func TestMiddlewareMatchers(t *testing.T) {
	dir := project(t, map[string]string{
		"proxy.ts": `import { NextResponse } from "next/server";
export function proxy() { return NextResponse.next(); }
export const config = { matcher: ["/dashboard/:path*", { source: "/api/:path*", has: [{ type: "header", key: "x-auth" }] }] };`,
	})
	h := &Host{Dir: dir, Out: io.Discard}
	s, err := h.LoadNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ms, err := h.MiddlewareMatchers(context.Background(), filepath.Join(dir, "proxy.ts"), filepath.Join(dir, "node_modules", "next"), s.Config)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0]["originalSource"] != "/dashboard/:path*" || ms[1]["has"] == nil {
		t.Fatalf("matchers = %v", ms)
	}
	t.Logf("%v", ms[0]["regexp"])
}
