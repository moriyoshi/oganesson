// node:vm: unenv's module with runInThisContext, which config loaders use
// to evaluate code they transpiled (jiti, for Prisma's prisma.config.ts):
// in the current realm's global scope, as an indirect eval runs code.
// There is no second realm for new contexts: a context is its sandbox
// object, and code run in it sees the sandbox's properties first (a with
// scope), then this realm's globals, which is what modules evaluated by
// the eval package rely on (vanilla-extract's compiled stylesheets read
// module, exports and require from the sandbox).
"use strict";
const base = require("unenv:node/vm");

const globalEval = eval;

function runInThisContext(code) {
  return globalEval(String(code));
}

const contexts = new WeakSet();

function createContext(sandbox) {
  if (sandbox === undefined) sandbox = {};
  if (sandbox === null || (typeof sandbox !== "object" && typeof sandbox !== "function")) {
    throw Object.assign(new TypeError('The "contextObject" argument must be of type object.'), { code: "ERR_INVALID_ARG_TYPE" });
  }
  contexts.add(sandbox);
  return sandbox;
}

function isContext(object) {
  return (typeof object === "object" || typeof object === "function") && object !== null && contexts.has(object);
}

// A sloppy function: with is not allowed in strict code.
const runWith = new Function("__og_sandbox", "__og_code", "with (__og_sandbox) { return eval(__og_code); }");

function runInContext(code, context) {
  if (!isContext(context)) {
    throw Object.assign(new TypeError('The "contextifiedObject" argument must be an vm.Context'), { code: "ERR_INVALID_ARG_TYPE" });
  }
  return runWith(context, String(code));
}

function runInNewContext(code, sandbox) {
  return runWith(createContext(sandbox), String(code));
}

class Script {
  constructor(code, options) {
    this._code = String(code);
    this._options = options;
  }
  runInThisContext() {
    return globalEval(this._code);
  }
  runInContext(context) {
    return runInContext(this._code, context);
  }
  runInNewContext(sandbox) {
    return runInNewContext(this._code, sandbox);
  }
  createCachedData() {
    return new Uint8Array(0);
  }
}

module.exports = Object.assign({}, base, { runInThisContext, runInContext, runInNewContext, createContext, isContext, Script });
