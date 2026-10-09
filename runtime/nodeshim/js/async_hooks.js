// node:async_hooks: AsyncLocalStorage is native to the Oganesson engine.
"use strict";
const ALS = globalThis.AsyncLocalStorage;

class AsyncResource {
  constructor(type) {
    this.type = type;
    this._run = ALS.snapshot();
  }
  runInAsyncScope(fn, thisArg, ...args) { return this._run(() => fn.apply(thisArg, args)); }
  bind(fn) { return this.runInAsyncScope.bind(this, fn, undefined); }
  emitDestroy() { return this; }
  asyncId() { return 0; }
  triggerAsyncId() { return 0; }
  static bind(fn, type) { return new AsyncResource(type || "bound-anonymous-fn").bind(fn); }
}

// unenv's surface, with the engine's propagating AsyncLocalStorage.
module.exports = Object.assign({}, require("unenv:node/async_hooks"), {
  AsyncLocalStorage: ALS,
  AsyncResource,
  executionAsyncId: () => 0,
  triggerAsyncId: () => 0,
  createHook: () => ({ enable() { return this; }, disable() { return this; } }),
});
