package vfs_test

import (
	"context"
	"encoding/json"
	"io"
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

// TestNodeFS: node:fs over packaged files mounted at "/" (the runtime's
// cwd): relative and absolute reads, stat, readdir, read streams, and
// read-only errors.
func TestNodeFS(t *testing.T) {
	files := fstest.MapFS{
		"dist/client/index.html": {Data: []byte("<!--app-html-->")},
		"dist/client/a.txt":      {Data: []byte("0123456789")},
	}
	src := `
import fs from "node:fs";
import fsp from "node:fs/promises";
const out = [];
out.push(fs.readFileSync("./dist/client/index.html", "utf8"));
out.push(fs.existsSync("/dist/client/a.txt"), fs.existsSync("dist/missing"));
out.push(fs.statSync("dist/client").isDirectory(), fs.statSync("dist/client/a.txt").size);
out.push(fs.readdirSync("dist/client").sort().join("+"));
try { fs.writeFileSync("x.txt", "x"); } catch (e) { out.push(e.code); }
try { fs.readFileSync("nope"); } catch (e) { out.push(e.code); }
globalThis.result = (async () => {
  out.push(await fsp.readFile("dist/client/a.txt", "utf8"));
  const chunks = [];
  for await (const c of fs.createReadStream("dist/client/a.txt", { start: 2, end: 4 })) chunks.push(String(c));
  out.push(chunks.join(""));
  return out.join(",");
})();
`
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
	if err := vfs.Install(e, files); err != nil {
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
	if want := "<!--app-html-->,true,false,true,10,a.txt+index.html,EROFS,ENOENT,0123456789,234"; v.String() != want {
		t.Fatalf("got %s, want %s", v, want)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
