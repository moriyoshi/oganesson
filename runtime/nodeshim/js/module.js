// node:module subset. createRequire returns the host's require (build time)
// or one that throws: bundled code resolves its imports statically, and
// dynamic requires of optional packages are normally wrapped in try/catch.
"use strict";
const builtinModules = __OG_BUILTINS__;
function fallbackRequire(id) {
  const e = new Error("og: require(" + JSON.stringify(id) + ") is not available (bundle the dependency instead)");
  e.code = "MODULE_NOT_FOUND";
  throw e;
}
fallbackRequire.resolve = (id) => fallbackRequire(id);
fallbackRequire.cache = Object.create(null);
fallbackRequire.extensions = Object.create(null);
function createRequire(from) {
  const create = globalThis[Symbol.for("og.createRequire")]; // build-time loader
  if (create) return create(from);
  return typeof globalThis.require === "function" ? globalThis.require : fallbackRequire;
}
function Module(id = "", parent) {
  this.id = id;
  this.path = id ? id.replace(/[\\/][^\\/]*$/, "") || "." : ".";
  this.exports = {};
  this.filename = null;
  this.loaded = false;
  this.children = [];
  this.paths = [];
  if (parent && Array.isArray(parent.children)) parent.children.push(this);
}
// module._compile(content, filename): run CommonJS source as a module of
// that file, in Node's module wrapper (require-from-string, which
// babel-plugin-macros' preval.macro and config loaders use).
Module.prototype._compile = function _compile(content, filename) {
  if (this.filename === null) this.filename = filename;
  const dirname = String(filename).replace(/[\\/][^\\/]*$/, "") || ".";
  const req = createRequire(filename);
  const source = String(content).replace(/^#!.*/, "");
  const wrapper = new Function("exports", "require", "module", "__filename", "__dirname", source);
  const result = wrapper.call(this.exports, this.exports, req, this, filename, dirname);
  return result;
};
Module.createRequire = createRequire;
Module.builtinModules = builtinModules;
Module.isBuiltin = (name) => builtinModules.includes(String(name).replace(/^node:/, ""));
Module._extensions = fallbackRequire.extensions;
Module._cache = fallbackRequire.cache;
// Module._resolveFilename(request, parent, isMain, options): the file
// require(request) from parent loads (resolve-from, import-fresh), as the
// parent's require.resolve; options.paths are tried in order.
function _resolveFilename(request, parent, isMain, options) {
  if (options && Array.isArray(options.paths)) {
    let last;
    for (const p of options.paths) {
      try {
        return createRequire(String(p).replace(/[\\/]?$/, "/") + "noop.js").resolve(request);
      } catch (e) {
        last = e;
      }
    }
    if (last) throw last;
  }
  const from = parent && (parent.filename || parent.id) ? parent.filename || parent.id : (globalThis.process ? globalThis.process.cwd() : "") + "/noop.js";
  return createRequire(from).resolve(request);
};
// ESM loader hooks: Oganesson loads modules through esbuild and the CommonJS
// loader, never through Node's ESM loader, so there is nothing to hook.
// (Tailwind v4 registers a cache-busting loader at startup.)
function register() {}
// Module._nodeModulePaths(from): the node_modules directories a require
// from "from" searches, nearest first, as Node's (config loaders such as
// jiti use it): every ancestor's node_modules, skipping directories that
// are themselves named node_modules.
function _nodeModulePaths(from) {
  const path = require("path");
  from = path.resolve(String(from));
  if (from === "/") return ["/node_modules"];
  const out = [];
  for (let dir = from; ; dir = path.dirname(dir)) {
    if (path.basename(dir) !== "node_modules") out.push(path.join(dir, "node_modules"));
    if (dir === "/") break;
  }
  return out;
}
// Module._preloadModules(requests): require each request from the working
// directory, as node --require does (GraphQL Mesh's CLI preloads its
// --require modules through it). Anything but an array preloads nothing,
// as in Node 24.
function _preloadModules(requests) {
  if (!Array.isArray(requests)) return;
  const req = createRequire((globalThis.process ? globalThis.process.cwd() : "") + "/noop.js");
  for (const r of requests) req(r);
}
Object.assign(Module, require("unenv:node/module"), { createRequire, builtinModules, isBuiltin: Module.isBuiltin, register, _nodeModulePaths, _resolveFilename, _preloadModules });
delete Module.registerHooks;
module.exports = Module;
module.exports.Module = Module;
module.exports.createRequire = createRequire;
module.exports.builtinModules = builtinModules;
module.exports.isBuiltin = Module.isBuiltin;
module.exports.register = register;
