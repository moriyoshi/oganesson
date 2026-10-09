package jshost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/moriyoshi/oganesson/runtime/engine"
)

// RunProgram runs a Node program (a package's CLI, such as prisma's
// build/index.js) on the build host, as `node file args...` would in h.Dir:
// process.argv is set, process.exit(code) ends it, and, as Node does, so
// does an event loop with nothing left to do (the code is then
// process.exitCode, 0 by default). An uncaught error fails it.
func (h *Host) RunProgram(ctx context.Context, file string, args []string) (int, error) {
	code, err := h.runProgram(ctx, file, args, false)
	if err != nil && strings.Contains(err.Error(), "Top-level await is not available") {
		// An ES module awaiting at its top level (Prisma 8's CLI): Node runs
		// such an entry as a module, which require() cannot load; bundled
		// in og's top-level-await mode, with each file's own paths
		// (fileLocalsPlugin), it runs.
		return h.runProgram(ctx, file, args, true)
	}
	return code, err
}

func (h *Host) runProgram(ctx context.Context, file string, args []string, bundled bool) (int, error) {
	argv, _ := json.Marshal(append([]string{"/usr/local/bin/node", file}, args...))
	fileJSON, _ := json.Marshal(file)
	load := "require(file);"
	if bundled {
		load = "await import(" + string(fileJSON) + ");"
	}
	entry := `
globalThis.__ogProgram = (async () => {
  const setCode = globalThis.__ogSetExitCode;
  let exitCode;
  Object.defineProperty(process, "exitCode", { get: () => exitCode, set: (v) => { exitCode = v; setCode(Number(v) || 0); }, configurable: true });
  process.argv = ` + string(argv) + `;
  let finish;
  const exited = new Promise((resolve) => (finish = resolve));
  const marker = Symbol.for("og.processExit");
  process.exit = (code) => {
    if (code !== undefined) process.exitCode = code;
    finish(process.exitCode ?? 0);
    throw Object.assign(new Error("og: process.exit"), { [marker]: true });
  };
  // A computed specifier: the program loads at run time, as Node loads it
  // (bundled, its files' own paths and assets would be lost).
  const file = ` + string(fileJSON) + `;
  try {
    ` + load + `
  } catch (e) {
    if (!e || !e[marker]) throw e;
  }
  return exited;
})();
`
	host := h
	if bundled {
		hc := *h
		hc.TopLevelAwait = true
		host = &hc
	}
	code, err := host.Bundle("", entry, "")
	if err != nil {
		return 1, err
	}
	exitCode := 0
	v, err := host.EvalWith(ctx, code, "__ogProgram", func(e *engine.Engine) error {
		return e.VM.Set("__ogSetExitCode", func(n int) { exitCode = n })
	})
	switch {
	case errors.Is(err, engine.ErrStalled):
		return exitCode, nil // the loop went idle: Node exits
	case err != nil:
		return 1, err
	}
	switch n := v.(type) {
	case int64:
		return int(n), nil
	case float64:
		return int(n), nil
	}
	return exitCode, nil
}
