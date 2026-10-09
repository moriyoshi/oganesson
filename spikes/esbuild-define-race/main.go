// Spike: reproduce the esbuild data race in NumberRenamer seen in Vite
// builds (Rolldown facade), without Vite or Oganesson code.
//
// Result (2026-10-07, see JOURNAL): esbuild v0.28.2 races (go run -race)
// when an object-valued define (process.env = {}, which Vite defines for
// client builds) combines with code splitting and unminified identifiers.
// Two goroutines of AssignNamesByScope (linker.renameSymbolsInChunk) write
// the same entry of the renamer's names table. Without the define, or with
// MinifyIdentifiers, there is no race.
//
//	go run -race ./spikes/esbuild-define-race testdata/apps/vite-react
//
// (any directory with react and react-dom installed).
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/evanw/esbuild/pkg/api"
)

func main() {
	dir, _ := filepath.Abs(os.Args[1])
	files := map[string]string{
		"entry.js": `import { createRoot } from "react-dom/client"; import("./lazy.js").then((m) => m.b()); console.log(createRoot);`,
		"lazy.js":  `import { useState } from "react"; export function b() { return useState; }`,
	}
	for i := 0; i < 5; i++ {
		res := api.Build(api.BuildOptions{
			EntryPoints:   []string{"entry.js"},
			AbsWorkingDir: dir,
			Bundle:        true,
			Splitting:     true,
			Format:        api.FormatESModule,
			Outdir:        "out",
			Write:         false,
			Define:        map[string]string{"process.env": "{}"},
			Plugins: []api.Plugin{{Name: "mem", Setup: func(b api.PluginBuild) {
				b.OnResolve(api.OnResolveOptions{Filter: `^(\./)?(entry|lazy)\.js$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
					return api.OnResolveResult{Path: filepath.Base(a.Path), Namespace: "mem"}, nil
				})
				b.OnLoad(api.OnLoadOptions{Filter: ".*", Namespace: "mem"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
					c := files[a.Path]
					return api.OnLoadResult{Contents: &c, ResolveDir: dir}, nil
				})
			}}},
		})
		if len(res.Errors) > 0 {
			fmt.Println(res.Errors[0].Text)
			os.Exit(1)
		}
	}
	fmt.Println("built 5 times")
}
