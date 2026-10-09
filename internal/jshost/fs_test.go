package jshost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fs surface build tools use beyond the basics (TanStack Router's
// generator): BigInt stats, chmod / chown, and fs.promises.open's
// FileHandle with Node's flag semantics.
func TestFsFileHandleAndBigIntStats(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
import fsp from "node:fs/promises";
import fs from "node:fs";
globalThis.__ogResult = (async () => {
  const out = [];
  const fh = await fsp.open(`+jsonString(filepath.Join(dir, "a.txt"))+`, "r");
  const st = await fh.stat({ bigint: true });
  out.push(typeof st.mtimeMs, typeof st.mtimeNs, st.mtimeNs / 1000000n === st.mtimeMs, typeof st.size, String(st.size));
  out.push((await fh.readFile()).toString());
  await fh.close();
  try { await fsp.open(`+jsonString(filepath.Join(dir, "missing.txt"))+`, "r"); out.push("opened"); } catch (e) { out.push(e.code); }
  const w = await fsp.open(`+jsonString(filepath.Join(dir, "b.txt"))+`, "w");
  await w.writeFile("written");
  await w.close();
  out.push(fs.readFileSync(`+jsonString(filepath.Join(dir, "b.txt"))+`, "utf8"));
  await fsp.chmod(`+jsonString(filepath.Join(dir, "b.txt"))+`, 0o600);
  out.push((fs.statSync(`+jsonString(filepath.Join(dir, "b.txt"))+`).mode & 0o777).toString(8));
  await fsp.chown(`+jsonString(filepath.Join(dir, "b.txt"))+`, 0, 0);
  try { await fsp.open(`+jsonString(filepath.Join(dir, "b.txt"))+`, "wx"); out.push("overwrote"); } catch (e) { out.push(e.code); }
  return out.join(",");
})();
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if want := "bigint,bigint,true,bigint,5,hello,ENOENT,written,600,EEXIST"; v != want {
		t.Fatalf("got %v, want %s", v, want)
	}
}

// TestFsRelativePaths: relative paths resolve against the project, which
// build-time JS sees as process.cwd() (TanStack Start writes its sitemap to
// "dist/client/sitemap.xml"), not against og's own working directory.
func TestFsRelativePaths(t *testing.T) {
	dir := t.TempDir()
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
import fs from "node:fs";
globalThis.__ogResult = (async () => {
  fs.mkdirSync("out/sub", { recursive: true });
  fs.writeFileSync("out/sub/a.txt", "rel");
  fs.renameSync("out/sub/a.txt", "out/b.txt");
  return [process.cwd() === `+jsonString(dir)+`, fs.existsSync("out/b.txt"), fs.readFileSync("out/b.txt", "utf8"), fs.readdirSync("out").sort().join("+")].join(",");
})();
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if want := "true,true,rel,b.txt+sub"; v != want {
		t.Fatalf("got %v, want %s", v, want)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "out", "b.txt")); err != nil || string(b) != "rel" {
		t.Fatalf("not written under the project: %q %v", b, err)
	}
}

// TestFsDescriptors: file descriptors (openSync / readSync / writeSync /
// fstatSync / closeSync) over the host's whole-file operations, read the
// way @sveltejs/adapter-node hashes assets (a buffer at a time).
func TestFsDescriptors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
import fs from "node:fs";
const a = `+jsonString(filepath.Join(dir, "a.txt"))+`, b = `+jsonString(filepath.Join(dir, "b.txt"))+`;
const out = [];
let fd = fs.openSync(a, "r");
const buf = Buffer.alloc(4);
const chunks = [];
let n;
while ((n = fs.readSync(fd, buf)) > 0) chunks.push(buf.subarray(0, n).toString());
out.push(chunks.join("|"));
const at = Buffer.alloc(5);
out.push(fs.readSync(fd, at, 0, 5, 6), at.toString(), fs.fstatSync(fd).size);
fs.closeSync(fd);
try { fs.readSync(fd, buf); } catch (e) { out.push(e.code); }
fd = fs.openSync(b, "w");
fs.writeSync(fd, "abc");
fs.writeSync(fd, Buffer.from("XYZ"), 0, 2);
fs.writeSync(fd, "-", 0);
fs.closeSync(fd);
out.push(fs.readFileSync(b, "utf8"));
fd = fs.openSync(b, "a");
fs.writeSync(fd, "!");
fs.closeSync(fd);
out.push(fs.readFileSync(b, "utf8"));
try { fs.openSync(`+jsonString(filepath.Join(dir, "missing"))+`); } catch (e) { out.push(e.code); }
globalThis.__ogResult = out.join(",");
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if want := "hell|o wo|rld,5,world,11,EBADF,-bcXY,-bcXY!,ENOENT"; v != want {
		t.Fatalf("got %v, want %s", v, want)
	}
}

// TestStdoutBypassesConsole: process.stdout.write goes to the host's
// output, not through console.log, so a logger that wraps both (consola:
// console.log logs through stdout's saved write) does not recurse.
func TestStdoutBypassesConsole(t *testing.T) {
	var out strings.Builder
	h := &Host{Dir: t.TempDir(), Out: &out}
	code, err := h.Bundle("", `
const write = process.stdout.write.bind(process.stdout);
console.log = (...args) => write(args.join(" ") + "\n");
process.stdout.write = (data) => { console.log("wrapped " + String(data).trim()); return true; };
process.stdout.write("hello\n");
process.stderr.write(new TextEncoder().encode("bytes\n"));
globalThis.__ogResult = "done";
`, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Eval(context.Background(), code, "__ogResult"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "wrapped hello\nbytes\n" {
		t.Fatalf("output %q", got)
	}
}

// TestFsCp: fs.cp / cpSync / promises.cp copy files and, with recursive,
// directories (Nitro copies its public assets this way).
func TestFsCp(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"src/a.txt": "a", "src/sub/b.txt": "b", "src/skip.txt": "s"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("src/a.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
import fs from "node:fs";
import fsp from "node:fs/promises";
const d = `+jsonString(dir)+`;
globalThis.__ogResult = (async () => {
  const out = [];
  try { fs.cpSync(d + "/src", d + "/x"); } catch (e) { out.push(e.code); }
  fs.cpSync(d + "/src", d + "/copy", { recursive: true, filter: (from) => !from.endsWith("skip.txt") });
  await fsp.cp(d + "/src/a.txt", d + "/one.txt");
  await fsp.cp(d + "/src/sub/b.txt", d + "/new/dirs/b.txt");
  out.push(fs.readlinkSync(d + "/link"), await fsp.readlink(d + "/link"));
  try { fs.readlinkSync(d + "/src/a.txt"); } catch (e) { out.push(e.code); }
  out.push(fs.readFileSync(d + "/copy/sub/b.txt", "utf8"), fs.existsSync(d + "/copy/skip.txt"), fs.readFileSync(d + "/one.txt", "utf8"), fs.readFileSync(d + "/new/dirs/b.txt", "utf8"));
  return out.join(",");
})();
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if v != "ERR_FS_EISDIR,src/a.txt,src/a.txt,EINVAL,b,false,a,b" {
		t.Fatalf("got %v", v)
	}
}

// TestFsGlob: fs.globSync / glob / promises.glob (Node 22+), with the
// results Node 24 gives on the same tree (SolidStart finds its app entry
// with globSync("…/app.{j,t}sx")).
func TestFsGlob(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"src/app.tsx", "src/app.jsx", "src/app.ts", "src/a/x.ts", "src/a/b/y.ts", "src/c/z.js", ".hidden/h.ts", "top.md"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
import fs from "node:fs";
const s = (a) => a.slice().sort().join(",");
const cwd = process.cwd();
globalThis.__ogResult = (async () => {
  const out = [];
  out.push(s(fs.globSync("src/app.{j,t}sx")));
  out.push(s(fs.globSync("**/*.ts")));
  out.push(s(fs.globSync("src/*")));
  out.push(s(fs.globSync(cwd + "/src/a/**/*.ts")).replaceAll(cwd, "CWD"));
  out.push(s(fs.globSync("**/*.ts", { exclude: ["src/a/**"] })));
  out.push(s(fs.globSync("src/?/*.ts")));
  out.push(s(fs.globSync("*.md", { withFileTypes: true }).map((d) => d.name + ":" + d.isFile())));
  const r = [];
  for await (const f of fs.promises.glob("src/c/*")) r.push(f);
  out.push(s(r));
  return out.join("\n");
})();
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"src/app.jsx,src/app.tsx",
		"src/a/b/y.ts,src/a/x.ts,src/app.ts",
		"src/a,src/app.jsx,src/app.ts,src/app.tsx,src/c",
		"CWD/src/a/b/y.ts,CWD/src/a/x.ts",
		"src/app.ts",
		"src/a/x.ts",
		"top.md:true",
		"src/c/z.js",
	}, "\n")
	if v != want {
		t.Fatalf("got\n%v\nwant\n%s", v, want)
	}
}

// TestFsStreams: fs.createReadStream / createWriteStream are Node's own
// ReadStream / WriteStream (lib/internal/fs/streams.js) over og's fd
// operations; before, createWriteStream was unenv's not-implemented stub
// and createReadStream a plain Readable. The expectations are Node 24's on
// the same steps.
func TestFsStreams(t *testing.T) {
	dir := t.TempDir()
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `import fs from "node:fs";
const dir = `+jsonString(dir)+`;
const out = [];
const done = (s) => new Promise((res, rej) => { s.on("close", res); s.on("error", rej); });
globalThis.result = (async () => {
  const p = dir + "/a.txt";
  const ev = [];
  const w = fs.createWriteStream(p);
  out.push("pending:" + w.pending, "isWS:" + (w instanceof fs.WriteStream));
  for (const e of ["open", "ready", "finish", "close"]) w.on(e, () => ev.push(e));
  w.write("hello ");
  w.write(Buffer.from("world"));
  w.end("!");
  await done(w);
  out.push("events:" + ev.join(">"), "written:" + w.bytesWritten, "content:" + fs.readFileSync(p, "utf8"));
  const a = fs.createWriteStream(p, { flags: "a" });
  a.end(" more");
  await done(a);
  out.push("append:" + fs.readFileSync(p, "utf8"));
  const chunks = [];
  const r = fs.createReadStream(p, { highWaterMark: 4 });
  out.push("isRS:" + (r instanceof fs.ReadStream));
  r.on("data", (c) => chunks.push(c.length));
  await done(r);
  out.push("chunks:" + chunks.join("/"), "read:" + r.bytesRead);
  const parts = [];
  const s = fs.createReadStream(p, { start: 2, end: 6, encoding: "utf8" });
  s.on("data", (c) => parts.push(c));
  await done(s);
  out.push("range:" + JSON.stringify(parts.join("")));
  const e = fs.createReadStream(dir + "/missing.txt");
  const err = await new Promise((res) => e.on("error", res));
  out.push("err:" + err.code + ":" + err.syscall);
  let text = "";
  for await (const c of fs.createReadStream(p, "utf8")) text += c;
  out.push("iter:" + text.length);
  return out.join(" | ");
})();
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "result")
	if err != nil {
		t.Fatal(err)
	}
	want := "pending:true | isWS:true | events:open>ready>finish>close | written:12 | content:hello world! | append:hello world! more | isRS:true | chunks:4/4/4/4/1 | read:17 | range:\"llo w\" | err:ENOENT:open | iter:17"
	if v != want {
		t.Fatalf("got\n%v\nwant\n%s", v, want)
	}
}
