// node:fs, read-only. Backed by the host's filesystem at build time
// (Symbol.for("og.fs")); unavailable when no host provides it.
"use strict";
// Extends unenv's fs (complete API surface) with real operations backed by
// the build-time host; without a host, unenv's stubs remain.
const base = require("unenv:node/fs");
const host = globalThis[Symbol.for("og.fs")];


function fsError(rec) {
  const e = new Error(rec.message);
  e.code = rec.error;
  e.errno = -1;
  e.syscall = rec.syscall;
  e.path = rec.path;
  return e;
}
function call(fn, ...args) {
  if (!host) {
    const e = new Error("og: node:fs is not available in this runtime");
    e.code = "ENOSYS";
    throw e;
  }
  const r = host[fn](...args);
  if (r && typeof r === "object" && typeof r.error === "string") throw fsError(r);
  return r;
}
function toPath(p) {
  if (p instanceof URL) return decodeURIComponent(p.pathname);
  if (ArrayBuffer.isView(p)) return new TextDecoder().decode(p);
  return String(p);
}
function readOnly(name) {
  return () => {
    const e = new Error(`EROFS: read-only file system, ${name}`);
    e.code = "EROFS";
    throw e;
  };
}

class Stats {
  constructor(r, bigint) {
    this.size = r.size;
    this.mode = r.mode;
    this.mtimeMs = r.mtimeMs;
    this.mtime = new Date(r.mtimeMs);
    this.atimeMs = this.ctimeMs = this.birthtimeMs = r.mtimeMs;
    this.atime = this.ctime = this.birthtime = this.mtime;
    if (bigint) {
      // BigIntStats: integer fields as BigInt, plus the *Ns fields.
      const ns = BigInt(r.mtimeNs ?? Math.round(r.mtimeMs * 1e6));
      this.size = BigInt(r.size);
      this.mode = BigInt(r.mode);
      this.mtimeMs = this.atimeMs = this.ctimeMs = this.birthtimeMs = ns / 1000000n;
      this.mtimeNs = this.atimeNs = this.ctimeNs = this.birthtimeNs = ns;
    }
    this._r = r;
  }
  isFile() { return this._r.isFile; }
  isDirectory() { return this._r.isDirectory; }
  isSymbolicLink() { return this._r.isSymbolicLink; }
  isFIFO() { return false; }
  isSocket() { return false; }
  isBlockDevice() { return false; }
  isCharacterDevice() { return false; }
}

class Dirent {
  constructor(r, dir) { this.name = r.name; this.parentPath = this.path = dir; this._r = r; }
  isFile() { return this._r.isFile; }
  isDirectory() { return this._r.isDirectory; }
  isSymbolicLink() { return this._r.isSymbolicLink; }
  isFIFO() { return false; }
  isSocket() { return false; }
  isBlockDevice() { return false; }
  isCharacterDevice() { return false; }
}

function encodingOf(opts) {
  return typeof opts === "string" ? opts : opts && opts.encoding;
}

let BufferCtor;
function toBuffer(ab) {
  if (BufferCtor === undefined) {
    try { BufferCtor = require("buffer").Buffer; } catch { BufferCtor = null; }
  }
  return BufferCtor ? BufferCtor.from(ab) : new Uint8Array(ab);
}

const fs = {
  constants: { F_OK: 0, R_OK: 4, W_OK: 2, X_OK: 1 },
  Stats,
  Dirent,
  existsSync(p) { try { call("stat", toPath(p), true); return true; } catch { return false; } },
  statSync(p, opts) {
    try { return new Stats(call("stat", toPath(p), true), opts && opts.bigint); }
    catch (e) { if (opts && opts.throwIfNoEntry === false && e.code === "ENOENT") return undefined; throw e; }
  },
  lstatSync(p, opts) {
    try { return new Stats(call("stat", toPath(p), false), opts && opts.bigint); }
    catch (e) { if (opts && opts.throwIfNoEntry === false && e.code === "ENOENT") return undefined; throw e; }
  },
  readFileSync(p, opts) {
    const ab = call("readFile", toPath(p));
    const enc = encodingOf(opts);
    if (enc) return new TextDecoder(enc === "utf8" ? "utf-8" : enc).decode(ab);
    return toBuffer(ab);
  },
  readdirSync(p, opts) {
    const dir = toPath(p);
    const entries = call("readdir", dir);
    return opts && opts.withFileTypes ? entries.map((r) => new Dirent(r, dir)) : entries.map((r) => r.name);
  },
  realpathSync(p) { return call("realpath", toPath(p)); },
  // A symlink's target (EINVAL for anything else, as readlink(2)); hosts
  // without symlinks (the packaged files) have none.
  readlinkSync(p) {
    if (!host || !host.readlink) {
      call("stat", toPath(p), false);
      const e = new Error(`EINVAL: invalid argument, readlink '${toPath(p)}'`);
      e.code = "EINVAL"; e.errno = -22; e.syscall = "readlink"; e.path = toPath(p);
      throw e;
    }
    return call("readlink", toPath(p));
  },
  accessSync(p) { call("stat", toPath(p), true); },
  // Writes need a host that allows them (build time: inside the project).
  writeFileSync(p, data, opts) {
    if (!host || !host.writeFile) return readOnly("write")();
    const bytes = typeof data === "string" ? new TextEncoder().encode(data) : new Uint8Array(data.buffer ? data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength) : data);
    call("writeFile", toPath(p), bytes, !!(opts && opts.flag === "a"));
  },
  appendFileSync(p, data) {
    if (!host || !host.writeFile) return readOnly("append")();
    const bytes = typeof data === "string" ? new TextEncoder().encode(data) : new Uint8Array(data);
    call("writeFile", toPath(p), bytes, true);
  },
  mkdirSync(p, opts) {
    if (!host || !host.mkdir) return readOnly("mkdir")();
    call("mkdir", toPath(p), !!(opts && opts.recursive));
  },
  rmSync(p, opts) {
    if (!host || !host.rm) return readOnly("rm")();
    const recursive = !!(opts && opts.recursive);
    try {
      // As Node's: a directory needs recursive.
      if (!recursive && call("stat", toPath(p), false).isDirectory) {
        const e = new Error(`Path is a directory: rm returned EISDIR (is a directory) ${toPath(p)}`);
        e.code = "ERR_FS_EISDIR";
        e.errno = 21;
        e.syscall = "rm";
        e.path = toPath(p);
        throw e;
      }
      call("rm", toPath(p), recursive);
    } catch (e) {
      if (!(opts && opts.force && e.code === "ENOENT")) throw e;
    }
  },
  unlinkSync(p) {
    if (!host || !host.rm) return readOnly("unlink")();
    if (call("stat", toPath(p), false).isDirectory) throw fsError({ error: "EISDIR", message: `EISDIR: illegal operation on a directory, unlink '${toPath(p)}'`, syscall: "unlink", path: toPath(p) });
    call("rm", toPath(p), false);
  },
  rmdirSync(p, opts) {
    if (!host || !host.rm) return readOnly("rmdir")();
    const st = call("stat", toPath(p), false);
    if (!st.isDirectory) throw fsError({ error: "ENOTDIR", message: `ENOTDIR: not a directory, rmdir '${toPath(p)}'`, syscall: "rmdir", path: toPath(p) });
    call("rm", toPath(p), !!(opts && opts.recursive));
  },
  renameSync(from, to) {
    if (!host || !host.rename) return readOnly("rename")();
    call("rename", toPath(from), toPath(to));
  },
  copyFileSync(from, to) { fs.writeFileSync(to, fs.readFileSync(from)); },
  // fs.cp (Node 16.7+): files, and directories with recursive.
  cpSync(src, dest, opts = {}) {
    const from = toPath(src), to = toPath(dest);
    if (opts.filter && !opts.filter(from, to)) return;
    const st = fs.statSync(from);
    if (st.isDirectory()) {
      if (!opts.recursive) {
        const e = new Error(`Recursive option is required to copy a directory: ${from}`);
        e.code = "ERR_FS_EISDIR";
        throw e;
      }
      fs.mkdirSync(to, { recursive: true });
      for (const name of fs.readdirSync(from)) fs.cpSync(from + "/" + name, to + "/" + name, opts);
      return;
    }
    if (fs.existsSync(to)) {
      if (opts.errorOnExist && opts.force === false) {
        const e = new Error(`Target already exists: cp returned EEXIST (${to} already exists)`);
        e.code = "ERR_FS_CP_EEXIST";
        throw e;
      }
      if (opts.force === false) return;
    }
    // The destination's parent directories are created, as Node's cp does.
    const parent = to.slice(0, to.lastIndexOf("/"));
    if (parent && !fs.existsSync(parent)) fs.mkdirSync(parent, { recursive: true });
    fs.writeFileSync(to, fs.readFileSync(from));
  },
  chmodSync(p, mode) {
    if (!host || !host.chmod) return readOnly("chmod")();
    call("chmod", toPath(p), typeof mode === "string" ? parseInt(mode, 8) : mode);
  },
  // Ownership cannot change here; the file must exist, as for chown(2).
  chownSync(p) { call("stat", toPath(p), true); },
  watch: readOnly("watch"),
};
fs.realpathSync.native = fs.realpathSync;

// File descriptors over the path-based host: a read descriptor holds the
// file's contents and a position; a write descriptor buffers its writes
// and writes the file on close (and fsync). Flags as fs.promises.open's.
const fds = new Map();
let nextFd = 100;
function fdOf(fd) {
  const f = fds.get(fd);
  if (!f) {
    const e = new Error(`EBADF: bad file descriptor`);
    e.code = "EBADF"; e.syscall = "read";
    throw e;
  }
  return f;
}
function flushFd(f) {
  if (f.dirty) {
    fs.writeFileSync(f.path, f.data);
    f.dirty = false;
  }
}
Object.assign(fs, {
  openSync(p, flags = "r") {
    const path = toPath(p);
    const fl = typeof flags === "number" ? "r" : String(flags);
    const exists = fs.existsSync(path);
    if (fl.includes("x") && exists) {
      const e = new Error(`EEXIST: file already exists, open '${path}'`);
      e.code = "EEXIST"; e.syscall = "open"; e.path = path;
      throw e;
    }
    let data;
    if (fl.startsWith("w")) data = new Uint8Array(0);
    else if (exists) data = new Uint8Array(fs.readFileSync(path));
    else if (fl.startsWith("a")) data = new Uint8Array(0);
    else {
      // The file is missing: Node's error names the open.
      const e = new Error(`ENOENT: no such file or directory, open '${path}'`);
      e.errno = -2; e.code = "ENOENT"; e.syscall = "open"; e.path = path;
      throw e;
    }
    const writable = !fl.startsWith("r") || fl.includes("+");
    const fd = nextFd++;
    fds.set(fd, { path, data, pos: fl.startsWith("a") ? data.length : 0, append: fl.startsWith("a"), writable, dirty: writable && !exists || fl.startsWith("w") });
    if (writable) flushFd(fds.get(fd));
    return fd;
  },
  readSync(fd, buffer, offsetOrOptions, length, position) {
    const f = fdOf(fd);
    let offset = offsetOrOptions;
    if (offsetOrOptions && typeof offsetOrOptions === "object") ({ offset = 0, length, position = null } = offsetOrOptions);
    offset = offset ?? 0;
    length = length ?? buffer.byteLength - offset;
    const at = position == null || position < 0 ? f.pos : Number(position);
    const n = Math.max(0, Math.min(length, f.data.length - at));
    new Uint8Array(buffer.buffer, buffer.byteOffset, buffer.byteLength).set(f.data.subarray(at, at + n), offset);
    if (position == null || position < 0) f.pos += n;
    return n;
  },
  writeSync(fd, data, offsetOrPosition, lengthOrEncoding, position) {
    const f = fdOf(fd);
    let bytes, at;
    if (typeof data === "string") {
      bytes = new TextEncoder().encode(data);
      at = offsetOrPosition;
    } else {
      const view = new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
      const offset = offsetOrPosition ?? 0;
      bytes = view.subarray(offset, offset + (lengthOrEncoding ?? view.length - offset));
      at = position;
    }
    const start = f.append || at == null ? f.pos : Number(at);
    if (start + bytes.length > f.data.length) {
      const grown = new Uint8Array(start + bytes.length);
      grown.set(f.data);
      f.data = grown;
    }
    f.data.set(bytes, start);
    if (f.append || at == null) f.pos = start + bytes.length;
    f.dirty = true;
    return bytes.length;
  },
  fstatSync(fd, opts) {
    const f = fdOf(fd);
    flushFd(f);
    return fs.statSync(f.path, opts);
  },
  // writevSync(fd, buffers[, position]): the buffers in order, from
  // position (or the current position); the bytes written.
  writevSync(fd, buffers, position) {
    let n = 0;
    for (const b of buffers) {
      n += fs.writeSync(fd, b, 0, b.byteLength, position == null ? null : Number(position) + n);
    }
    return n;
  },
  fsyncSync(fd) { flushFd(fdOf(fd)); },
  fdatasyncSync(fd) { flushFd(fdOf(fd)); },
  closeSync(fd) {
    flushFd(fdOf(fd));
    fds.delete(fd);
  },
});

// fs.glob / globSync (Node 22+): braces, *, **, ?, [...] classes; the
// cwd, exclude (function or patterns) and withFileTypes options. Matches
// are relative to cwd for relative patterns, absolute for absolute ones.
function expandBraces(p) {
  const m = /\{([^{}]*)\}/.exec(p);
  if (!m) return [p];
  return m[1].split(",").flatMap((alt) => expandBraces(p.slice(0, m.index) + alt + p.slice(m.index + m[0].length)));
}
function segmentRegExp(seg) {
  let re = "";
  for (let i = 0; i < seg.length; i++) {
    const c = seg[i];
    if (c === "*") re += "[^/]*";
    else if (c === "?") re += "[^/]";
    else if (c === "[") {
      const end = seg.indexOf("]", i + 1);
      if (end < 0) { re += "\\["; continue; }
      let cls = seg.slice(i + 1, end);
      if (cls[0] === "!") cls = "^" + cls.slice(1);
      re += "[" + cls.replace(/\\/g, "\\\\") + "]";
      i = end;
    } else re += c.replace(/[.+^${}()|\\]/g, "\\$&");
  }
  // Like Node's glob, * does not match a leading dot.
  return new RegExp("^" + (seg.startsWith(".") ? "" : "(?!\\.)") + re + "$");
}
const isMagic = (seg) => /[*?[]/.test(seg);
function globOne(pattern, cwd, exclude, out) {
  const abs = pattern.startsWith("/");
  const segs = pattern.split("/").filter((s, i) => s !== "" || i === 0);
  let base = abs ? "/" : cwd;
  let i = abs ? 1 : 0;
  const prefix = [];
  for (; i < segs.length && !isMagic(segs[i]); i++) prefix.push(segs[i]);
  const join = (a, b) => (a.endsWith("/") ? a + b : a + "/" + b);
  for (const p of prefix) base = join(base, p);
  const rest = segs.slice(i);
  const shown = (full) => (abs ? full : full.slice(cwd.length + 1) || ".");
  if (!rest.length) {
    if (fs.existsSync(base)) out.add(shown(base));
    return;
  }
  const walk = (dir, k) => {
    if (k === rest.length) {
      if (!exclude(shown(dir))) out.add(shown(dir));
      return;
    }
    const seg = rest[k];
    let entries;
    try { entries = fs.readdirSync(dir, { withFileTypes: true }); } catch { return; }
    if (seg === "**") {
      walk(dir, k + 1);
      for (const e of entries) if (e.isDirectory() && !e.name.startsWith(".")) {
        const next = join(dir, e.name);
        if (!exclude(shown(next))) walk(next, k);
      }
      return;
    }
    const re = segmentRegExp(seg);
    for (const e of entries) {
      if (!re.test(e.name)) continue;
      const next = join(dir, e.name);
      if (k + 1 < rest.length && !e.isDirectory()) continue;
      walk(next, k + 1);
    }
  };
  walk(base, 0);
}
fs.globSync = (pattern, options = {}) => {
  const cwd = toPath(options.cwd ?? process.cwd()).replace(/\/$/, "") || "/";
  const ex = options.exclude;
  const excludeREs = Array.isArray(ex) ? ex.flatMap(expandBraces).map((p) => new RegExp("^" + p.split("/").map((s) => (s === "**" ? ".*" : segmentRegExp(s).source.slice(1, -1))).join("/") + "$")) : null;
  const exclude = (p) => (typeof ex === "function" ? !!ex(options.withFileTypes ? direntOf(p, cwd) : p) : excludeREs ? excludeREs.some((r) => r.test(p)) : false);
  const out = new Set();
  for (const p of [].concat(pattern)) for (const q of expandBraces(String(p))) globOne(q, cwd, exclude, out);
  const list = [...out];
  return options.withFileTypes ? list.map((p) => direntOf(p, cwd)) : list;
};
function direntOf(p, cwd) {
  const full = p.startsWith("/") ? p : cwd + "/" + p;
  const slash = full.lastIndexOf("/");
  const st = fs.lstatSync(full);
  const d = Object.create(Dirent.prototype);
  return Object.assign(d, { name: full.slice(slash + 1), parentPath: full.slice(0, slash) || "/", path: full.slice(0, slash) || "/", _st: st, isFile: () => st.isFile(), isDirectory: () => st.isDirectory(), isSymbolicLink: () => st.isSymbolicLink() });
}

// Callback and promise forms are built on the sync ones.
const asyncify = (fn) => (...args) => {
  const cb = typeof args[args.length - 1] === "function" ? args.pop() : null;
  let result, error = null;
  try { result = fn(...args); } catch (e) { error = e; }
  if (cb) queueMicrotask(() => cb(error, result));
};
for (const name of ["stat", "lstat", "readFile", "readdir", "realpath", "access", "writeFile", "appendFile", "mkdir", "rm", "rmdir", "unlink", "rename", "copyFile", "chmod", "chown", "cp", "readlink", "open", "close", "fstat", "fsync", "fdatasync"]) {
  fs[name] = asyncify(fs[name + "Sync"]);
}
// read and write call back with (err, bytes, buffer).
// writev(fd, buffers[, position], callback(err, bytesWritten, buffers)).
fs.writev = (fd, buffers, position, cb) => {
  if (typeof position === "function") [cb, position] = [position, null];
  let n, err = null;
  try { n = fs.writevSync(fd, buffers, position); } catch (e) { err = e; }
  queueMicrotask(() => (err ? cb(err) : cb(null, n, buffers)));
};
for (const name of ["read", "write"]) {
  const sync = fs[name + "Sync"];
  fs[name] = (fd, buffer, ...args) => {
    const cb = args.pop();
    let n, error = null;
    try { n = sync(fd, buffer, ...args); } catch (e) { error = e; }
    queueMicrotask(() => cb(error, n, buffer));
  };
}
fs.exists = (p, cb) => queueMicrotask(() => cb(fs.existsSync(p)));
fs.glob = (pattern, options, cb) => {
  if (typeof options === "function") (cb = options), (options = {});
  let r, error = null;
  try { r = fs.globSync(pattern, options); } catch (e) { error = e; }
  queueMicrotask(() => cb(error, r));
};
fs.realpath.native = fs.realpath;
fs.promises = {};
for (const name of ["stat", "lstat", "readFile", "readdir", "realpath", "access", "writeFile", "appendFile", "mkdir", "rm", "rmdir", "unlink", "rename", "copyFile", "chmod", "chown", "cp", "readlink"]) {
  const sync = fs[name + "Sync"];
  fs.promises[name] = async (...args) => sync(...args);
}

// fs.promises.open: a FileHandle over the path-based operations (no file
// descriptors here). Flags: r / r+ need the file; w truncates or creates;
// a creates; x fails if it exists.
class FileHandle {
  constructor(path, flags) { this._path = path; this._flags = flags; this.fd = -1; }
  async stat(opts) { return fs.statSync(this._path, opts); }
  async readFile(opts) { return fs.readFileSync(this._path, opts); }
  async writeFile(data, opts) { fs.writeFileSync(this._path, data, opts); }
  async appendFile(data) { fs.appendFileSync(this._path, data); }
  async chmod(mode) { fs.chmodSync(this._path, mode); }
  async truncate(len = 0) {
    if (len !== 0) throw new Error("og: FileHandle.truncate supports length 0 only");
    fs.writeFileSync(this._path, "");
  }
  async sync() {}
  async datasync() {}
  async close() {}
}
// fs.promises.glob is an async iterator of the matches.
fs.promises.glob = async function* (pattern, options) {
  yield* fs.globSync(pattern, options);
};
fs.promises.open = async (p, flags = "r") => {
  const path = toPath(p);
  const f = String(flags);
  const exists = fs.existsSync(path);
  if (f.includes("x") && exists) {
    const e = new Error(`EEXIST: file already exists, open '${path}'`);
    e.code = "EEXIST"; e.syscall = "open"; e.path = path;
    throw e;
  }
  if (f.startsWith("r")) {
    if (!exists) call("stat", path, true); // throws ENOENT
  } else if (f.startsWith("w")) {
    fs.writeFileSync(path, "");
  } else if (f.startsWith("a") && !exists) {
    fs.writeFileSync(path, "");
  }
  return new FileHandle(path, f);
};
fs.promises.FileHandle = FileHandle;
fs.promises.opendir = async (p) => {
  const entries = fs.readdirSync(p, { withFileTypes: true });
  return {
    async *[Symbol.asyncIterator]() { yield* entries; },
    async close() {},
  };
};

// fs.ReadStream / fs.WriteStream are Node's own (lib/internal/fs/streams.js,
// vendored), over the fd operations above (open / read / write / close /
// fsync): highWaterMark chunks, start / end, flags, autoClose, the open and
// ready events, bytesRead / bytesWritten. Loaded on first use.
let fsStreams;
const lazyStreams = () => (fsStreams ??= require("og-node-lib:internal/fs/streams"));
fs.createReadStream = (path, options) => new (lazyStreams().ReadStream)(path, options);
fs.createWriteStream = (path, options) => new (lazyStreams().WriteStream)(path, options);

module.exports = host ? Object.assign({}, base, fs, { promises: Object.assign({}, base.promises, fs.promises) }) : base;
if (host) {
  // Node defines these as lazy accessors on the module as well.
  for (const name of ["ReadStream", "WriteStream", "FileReadStream", "FileWriteStream"]) {
    Object.defineProperty(module.exports, name, {
      get: () => lazyStreams()[name.replace("File", "")],
      set(v) { Object.defineProperty(module.exports, name, { value: v, writable: true, enumerable: true, configurable: true }); },
      enumerable: true,
      configurable: true,
    });
  }
}
