package vfs_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/moriyoshi/oganesson/internal/gojatarget"
	"github.com/moriyoshi/oganesson/runtime/engine"
	"github.com/moriyoshi/oganesson/runtime/nodeshim"
	"github.com/moriyoshi/oganesson/runtime/vfs"
	"github.com/moriyoshi/oganesson/runtime/webapi"
)

// runOn runs src (which sets globalThis.result) in a fresh engine with
// node:fs over ov.
func runOn(t *testing.T, ov *vfs.Overlay, src string) string {
	t.Helper()
	bo := api.BuildOptions{
		Stdin:   &api.StdinOptions{Contents: "import " + quote(nodeshim.GlobalsModule) + ";\n" + src, Loader: api.LoaderJS},
		Bundle:  true,
		Write:   false,
		Plugins: []api.Plugin{nodeshim.Plugin(nodeshim.Options{})},
	}
	gojatarget.Apply(&bo)
	bo.Platform = api.PlatformBrowser
	res := api.Build(bo)
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors[0].Text)
	}
	e := engine.New(engine.Options{})
	defer e.Close()
	if _, err := webapi.Install(e, webapi.Options{Stdout: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if err := vfs.Install(e, ov); err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(10*time.Second, func() { e.VM.Interrupt("timeout") })
	defer timer.Stop()
	if _, err := e.VM.RunString(string(res.OutputFiles[0].Contents)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v, err := e.Await(ctx, e.VM.Get("result"))
	if err != nil {
		t.Fatal(err)
	}
	return v.String()
}

// overlayScenario exercises node:fs over packaged files (data/a.txt,
// data/b.txt, data/sub/c.txt) with writes: new files, appends, merged
// listings, directories created and removed, packaged files removed and
// written again, a directory renamed, and Node's errors.
const overlayScenario = `
const fs = require("node:fs");
const out = [];
const t = (label, f) => {
  try {
    const r = f();
    out.push(label + ": " + (r === undefined ? "ok" : JSON.stringify(r)));
  } catch (e) {
    out.push(label + ": " + e.code);
  }
};
t("read packaged", () => fs.readFileSync("data/a.txt", "utf8"));
t("write new", () => fs.writeFileSync("data/new.txt", "n"));
t("append", () => fs.appendFileSync("data/a.txt", "+"));
t("read appended", () => fs.readFileSync("data/a.txt", "utf8"));
t("readdir", () => fs.readdirSync("data").sort());
t("withFileTypes", () => fs.readdirSync("data", { withFileTypes: true }).map((d) => d.name + (d.isDirectory() ? "/" : "")).sort());
t("mkdir exists", () => fs.mkdirSync("data"));
t("mkdir -p", () => void fs.mkdirSync("x/y/z", { recursive: true }));
t("mkdir -p again", () => void fs.mkdirSync("x/y", { recursive: true }));
t("write no parent", () => fs.writeFileSync("nope/f.txt", "x"));
t("write over dir", () => fs.writeFileSync("data", "x"));
t("read dir", () => fs.readFileSync("data"));
t("rmdir not empty", () => fs.rmdirSync("data"));
t("rmdir file", () => fs.rmdirSync("data/a.txt"));
t("rmdir missing", () => fs.rmdirSync("missing"));
t("rm dir", () => fs.rmSync("x"));
t("unlink dir", () => fs.unlinkSync("x"));
t("rm -r", () => fs.rmSync("x", { recursive: true }));
t("x gone", () => fs.existsSync("x"));
t("rm missing force", () => fs.rmSync("missing", { force: true }));
t("rm missing", () => fs.rmSync("missing"));
t("unlink packaged", () => fs.unlinkSync("data/b.txt"));
t("gone", () => fs.existsSync("data/b.txt"));
t("rewrite gone", () => fs.writeFileSync("data/b.txt", "again"));
t("read again", () => fs.readFileSync("data/b.txt", "utf8"));
t("rm packaged dir", () => fs.rmSync("data/sub", { recursive: true }));
t("recreate dir", () => fs.mkdirSync("data/sub"));
t("recreated empty", () => fs.readdirSync("data/sub"));
t("rename dir", () => fs.renameSync("data", "moved"));
t("moved", () => fs.readdirSync("moved").sort());
t("old gone", () => fs.existsSync("data"));
t("stat size", () => fs.statSync("moved/a.txt").size);
t("stat dir", () => fs.statSync("moved").isDirectory());
t("copy", () => fs.copyFileSync("moved/a.txt", "copy.txt"));
t("copied", () => fs.readFileSync("copy.txt", "utf8"));
globalThis.result = (async () => {
  await fs.promises.writeFile("p.txt", "promise");
  out.push("promises: " + (await fs.promises.readFile("p.txt", "utf8")) + " " + (await fs.promises.readdir(".")).filter((n) => !n.endsWith(".js")).sort().join(","));
  try { await fs.promises.rmdir("moved"); } catch (e) { out.push("promises rmdir: " + e.code); }
  return out.join("\n");
})();
`

// overlayNode is Node 24's output for overlayScenario in a directory
// holding the same files, but for unlinking a directory: EPERM on macOS,
// where Node ran, EISDIR on Linux (og reports Linux).
const overlayNode = `
read packaged: "A"
write new: ok
append: ok
read appended: "A+"
readdir: ["a.txt","b.txt","new.txt","sub"]
withFileTypes: ["a.txt","b.txt","new.txt","sub/"]
mkdir exists: EEXIST
mkdir -p: ok
mkdir -p again: ok
write no parent: ENOENT
write over dir: EISDIR
read dir: EISDIR
rmdir not empty: ENOTEMPTY
rmdir file: ENOTDIR
rmdir missing: ENOENT
rm dir: ERR_FS_EISDIR
unlink dir: EISDIR
rm -r: ok
x gone: false
rm missing force: ok
rm missing: ENOENT
unlink packaged: ok
gone: false
rewrite gone: ok
read again: "again"
rm packaged dir: ok
recreate dir: ok
recreated empty: []
rename dir: ok
moved: ["a.txt","b.txt","new.txt","sub"]
old gone: false
stat size: 2
stat dir: true
copy: ok
copied: "A+"
promises: promise copy.txt,moved,p.txt
promises rmdir: ENOTEMPTY
`

// TestOverlay: the runtime's filesystem (packaged files under an in-memory
// writable layer) behaves as a directory on disk does under Node 24. A
// second engine on the same overlay sees the first one's writes, and the
// packaged files stay as they were.
func TestOverlay(t *testing.T) {
	lower := fstest.MapFS{
		"data/a.txt":     {Data: []byte("A")},
		"data/b.txt":     {Data: []byte("B")},
		"data/sub/c.txt": {Data: []byte("C")},
	}
	ov := vfs.NewOverlay(lower)
	if got, want := runOn(t, ov, overlayScenario), strings.TrimSpace(overlayNode); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	later := runOn(t, ov, `const fs = require("node:fs"); globalThis.result = Promise.resolve([fs.readFileSync("moved/a.txt", "utf8"), fs.existsSync("data"), fs.readFileSync("p.txt", "utf8")].join(","));`)
	if later != "A+,false,promise" {
		t.Errorf("second engine: %s", later)
	}
	if string(lower["data/a.txt"].Data) != "A" {
		t.Error("the packaged file changed")
	}
}
