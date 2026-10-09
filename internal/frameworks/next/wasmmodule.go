package next

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// WebAssembly and binary imports in the server build, standing in for
// webpack's `?module` (edge "asset/webassembly") handling: Next's edge
// dependencies (e.g. @vercel/og's resvg and yoga) import compiled modules as
// `./x.wasm?module`. The runtime's WebAssembly is internal/wasm in
// restricted mode.

const (
	wasmModuleSuffix = ".wasm?module"
	binaryPrefix     = "og-binary:" // + path relative to the importer: its bytes as a Uint8Array
)

func (b *builder) wasmPlugin() api.Plugin {
	return api.Plugin{Name: "og-wasm", Setup: func(pb api.PluginBuild) {
		pb.OnResolve(api.OnResolveOptions{Filter: `\.wasm\?module$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			return api.OnResolveResult{Path: filepath.Join(a.ResolveDir, strings.TrimSuffix(a.Path, "?module")), Namespace: "og-wasm-module"}, nil
		})
		pb.OnResolve(api.OnResolveOptions{Filter: `\.wasm$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			r, _ := b.wasmESMResolve(a)
			return r, nil
		})
		pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: wasmESMNamespace}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			code, err := wasmESMSource(a.Path)
			if err != nil {
				return api.OnLoadResult{}, err
			}
			return api.OnLoadResult{Contents: &code, Loader: api.LoaderJS, ResolveDir: filepath.Dir(a.Path)}, nil
		})
		pb.OnResolve(api.OnResolveOptions{Filter: `^og-binary:`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			return api.OnResolveResult{Path: filepath.Join(a.ResolveDir, strings.TrimPrefix(a.Path, binaryPrefix)), Namespace: "og-binary"}, nil
		})
		pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "og-wasm-module"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			code := "import bytes from " + jsonStr(binaryPrefix+filepath.Base(a.Path)) + ";\nexport default new WebAssembly.Module(bytes);\n"
			return api.OnLoadResult{Contents: &code, Loader: api.LoaderJS, ResolveDir: filepath.Dir(a.Path)}, nil
		})
		pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "og-binary"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			data, err := os.ReadFile(a.Path)
			if err != nil {
				return api.OnLoadResult{}, err
			}
			s := string(data)
			return api.OnLoadResult{Contents: &s, Loader: api.LoaderBinary}, nil
		})
	}}
}
