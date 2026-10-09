package next

import (
	"sort"

	"context"
	"encoding/json"
	"fmt"
	"github.com/evanw/esbuild/pkg/api"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// traceFiles are the project files the server reads at run time, as
// `output: "standalone"` ships them: what Next's file tracing
// (next/dist/compiled/@vercel/nft, which next build runs over its compiled
// server code with processCwd at the project) finds, plus next.config's
// outputFileTracingIncludes and minus its outputFileTracingExcludes (globs
// per route, matched with picomatch as Next matches them). og's bundle
// serves every route, so the routes' includes are united; an exclude
// applies when its key matches every route. Paths are relative to the
// project, as the runtime mounts them at its working directory.
//
// What is traced: the modules of the server that import fs or path (from
// the metafile of a build that keeps Node's built-ins external), each at its
// own path (TypeScript and JSX as esbuild's JavaScript), without following
// their imports of other modules, which the bundle holds. Tracing the whole
// bundle took a minute in the engine; the files found are the same, since
// only those modules can name files to read.
func (b *builder) traceFiles(ctx context.Context, traceMetafile, metafile string) (map[string][]byte, error) {
	sources, err := fsModules(traceMetafile, b.dir)
	if err != nil {
		return nil, err
	}
	entries := make([]string, 0, len(sources))
	for p := range sources {
		entries = append(entries, p)
	}
	sort.Strings(entries)
	entriesJSON, _ := json.Marshal(entries)
	sourcesJSON, _ := json.Marshal(sources)
	var routes []string
	for _, r := range b.routes {
		routes = append(routes, r.Pathname)
	}
	routesJSON, _ := json.Marshal(routes)
	includes, _ := json.Marshal(b.config["outputFileTracingIncludes"])
	excludes, _ := json.Marshal(b.config["outputFileTracingExcludes"])
	entry := `
import { nodeFileTrace } from "next/dist/compiled/@vercel/nft";
import globOrig from "next/dist/compiled/glob";
import picomatch from "next/dist/compiled/picomatch";
import fs from "node:fs/promises";
const dir = ` + jsonString(b.dir) + `;
const entries = ` + string(entriesJSON) + `;
const sources = ` + string(sourcesJSON) + `;
const routes = ` + string(routesJSON) + `;
const includes = ` + string(includes) + ` || {};
const excludes = ` + string(excludes) + ` || {};
const tolerate = (codes, fn) => async (p) => {
  try { return await fn(p); } catch (e) { if (codes.includes(e.code)) return null; throw e; }
};
const entrySet = new Set(entries);
const isModule = (p) => /\.(?:[cm]?[jt]sx?)$/.test(p);
const glob = (pattern) => new Promise((resolve, reject) =>
  globOrig(pattern, { cwd: dir, nodir: true, dot: true }, (err, files) => (err ? reject(err) : resolve(files))));
const matches = (key) => routes.filter((r) => picomatch(key, { dot: true, contains: true })(r));
globalThis.__ogResult = (async () => {
  const files = new Set();
  if (entries.length) {
    const { fileList } = await nodeFileTrace(entries, {
      base: dir,
      processCwd: dir,
      mixedModules: true,
      moduleSyncCatchall: true,
      // Other modules are in the bundle: not followed.
      ignore: (p) => isModule(p) && !entrySet.has(require("node:path").join(dir, p)),
      readFile: tolerate(["ENOENT", "EISDIR"], async (p) => sources[p] ?? (await fs.readFile(p, "utf8"))),
      readlink: tolerate(["EINVAL", "ENOENT", "UNKNOWN"], (p) => fs.readlink(p)),
      stat: tolerate(["ENOENT", "ENOTDIR"], (p) => fs.stat(p)),
    });
    for (const f of fileList) files.add(f);
  }
  for (const [key, list] of Object.entries(includes)) {
    if (!matches(key).length) continue;
    for (const g of list) for (const f of await glob(g.replace(/\\/g, "/"))) files.add(f);
  }
  const excluded = new Set();
  for (const [key, list] of Object.entries(excludes)) {
    if (matches(key).length !== routes.length) continue;
    for (const g of list) for (const f of await glob(g)) excluded.add(f);
  }
  return [...files].filter((f) => !excluded.has(f));
})();
`
	code, err := b.host.Bundle("", entry, "")
	if err != nil {
		return nil, fmt.Errorf("file tracing: %w", err)
	}
	v, err := b.host.Eval(ctx, code, "__ogResult")
	if err != nil {
		return nil, fmt.Errorf("file tracing: %w", err)
	}
	list, _ := v.([]any)
	bundled := bundledFiles(metafile, b.dir)
	out := map[string][]byte{}
	for _, x := range list {
		rel, _ := x.(string)
		if rel == "" {
			continue
		}
		rel = path.Clean(filepath.ToSlash(rel)) // globs give "./drizzle/..."
		abs := filepath.Join(b.dir, filepath.FromSlash(rel))
		if bundled[abs] {
			continue
		}
		// Outside the project (a monorepo's root): not under the runtime's cwd.
		if r, err := filepath.Rel(b.dir, abs); err != nil || strings.HasPrefix(r, "..") {
			continue
		}
		// package.json files nft lists for module resolution, which the
		// bundle has done already.
		if filepath.Base(abs) == "package.json" && strings.Contains(rel, "node_modules/") {
			continue
		}
		st, err := os.Stat(abs)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		out[filepath.ToSlash(rel)] = data
	}
	return out, nil
}

// bundledFiles are the files an esbuild metafile lists as inputs.
func bundledFiles(metafile, dir string) map[string]bool {
	var m struct {
		Inputs map[string]json.RawMessage `json:"inputs"`
	}
	out := map[string]bool{}
	if json.Unmarshal([]byte(metafile), &m) != nil {
		return out
	}
	for k := range m.Inputs {
		if i := strings.Index(k, ":"); i > 0 && !strings.Contains(k[:i], "/") {
			k = k[i+1:] // "ns:/abs/path"
		}
		if !filepath.IsAbs(k) {
			k = filepath.Join(dir, k)
		}
		out[filepath.Clean(k)] = true
	}
	return out
}

// jsonString is s as a JavaScript string literal.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// fsModules are the files among a metafile's inputs that import fs or
// path (Node's built-ins, external in that build), with their code as
// JavaScript for the tracer: TypeScript and JSX transformed by esbuild,
// imports kept.
func fsModules(metafile, dir string) (map[string]string, error) {
	var m struct {
		Inputs map[string]struct {
			Imports []struct {
				Path     string `json:"path"`
				External bool   `json:"external"`
			} `json:"imports"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(metafile), &m); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, in := range m.Inputs {
		uses := false
		for _, imp := range in.Imports {
			switch strings.TrimPrefix(imp.Path, "node:") {
			case "fs", "fs/promises", "path", "path/posix", "path/win32":
				uses = uses || imp.External
			}
		}
		if !uses {
			continue
		}
		if i := strings.Index(k, ":"); i > 0 && !strings.Contains(k[:i], "/") {
			k = k[i+1:]
		}
		if !filepath.IsAbs(k) {
			k = filepath.Join(dir, k)
		}
		src, err := os.ReadFile(k)
		if err != nil {
			continue // a virtual module
		}
		loader, ok := map[string]api.Loader{".ts": api.LoaderTS, ".mts": api.LoaderTS, ".cts": api.LoaderTS, ".tsx": api.LoaderTSX, ".jsx": api.LoaderJSX}[filepath.Ext(k)]
		if !ok {
			out[k] = string(src)
			continue
		}
		res := api.Transform(string(src), api.TransformOptions{Loader: loader, Format: api.FormatESModule, JSX: api.JSXPreserve, Sourcefile: k})
		if len(res.Errors) > 0 {
			continue
		}
		out[k] = string(res.Code)
	}
	return out, nil
}
