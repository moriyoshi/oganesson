import { host } from "./host";
import { defineGlobal } from "./util";
import { reportError } from "./console";

// Timeout mirrors Node's object so .ref()/.unref() work, while
// Symbol.toPrimitive keeps browser-style numeric use (clearTimeout(+id)) working.
class Timeout {
  #id: number;
  #ms: number;
  #repeat: boolean;
  #fn: () => void;
  #ref = true;
  #start: number;
  constructor(fn: () => void, ms: number, repeat: boolean) {
    this.#fn = fn;
    this.#ms = ms;
    this.#repeat = repeat;
    this.#start = performance.now();
    this.#id = host.timers.set(fn, ms, repeat);
    ids.set(this.#id, this);
  }
  get _id() { return this.#id; }
  // Node's _idleStart: when the timer was (re)armed, on the loop's clock;
  // it fires _idleStart + delay. Setting it moves a pending timer's due
  // time, as in Node (Next's createAtomicTimerGroup gives a group of
  // timers the first one's, so they fire together).
  get _idleStart() { return this.#start; }
  set _idleStart(v: number) {
    if (typeof v !== "number" || v === this.#start || !ids.has(this.#id)) return;
    this.#start = v;
    host.timers.clear(this.#id);
    ids.delete(this.#id);
    this.#id = host.timers.set(this.#fn, Math.max(0, v + this.#ms - performance.now()), this.#repeat);
    ids.set(this.#id, this);
    if (!this.#ref) host.timers.ref(this.#id, false);
  }
  ref() { this.#ref = true; host.timers.ref(this.#id, true); return this; }
  unref() { this.#ref = false; host.timers.ref(this.#id, false); return this; }
  hasRef() { return this.#ref; }
  refresh() {
    host.timers.clear(this.#id);
    ids.delete(this.#id);
    this.#start = performance.now();
    this.#id = host.timers.set(this.#fn, this.#ms, this.#repeat);
    ids.set(this.#id, this);
    if (!this.#ref) host.timers.ref(this.#id, false);
    return this;
  }
  close() { clear(this); return this; }
  [Symbol.toPrimitive]() { return this.#id; }
}

const ids = new Map<number, Timeout>();

function clear(t: unknown) {
  if (t == null) return;
  const id = t instanceof Timeout ? t._id : Number(t);
  host.timers.clear(id);
  ids.delete(id);
}

function wrap(fn: unknown, args: unknown[]): () => void {
  if (typeof fn !== "function") throw new TypeError("The callback argument must be of type function");
  return () => (fn as Function)(...args);
}

function delay(ms: unknown) {
  const n = Number(ms);
  return n > 0 && n <= 0x7fffffff ? n : (n > 0x7fffffff ? 1 : 0);
}

export function installTimers() {
  defineGlobal("setTimeout", function setTimeout(fn: unknown, ms?: unknown, ...args: unknown[]) {
    let t: Timeout;
    const run = wrap(fn, args);
    t = new Timeout(() => { ids.delete(t._id); run(); }, delay(ms), false);
    return t;
  });
  defineGlobal("setInterval", function setInterval(fn: unknown, ms?: unknown, ...args: unknown[]) {
    return new Timeout(wrap(fn, args), Math.max(1, delay(ms)), true);
  });
  defineGlobal("setImmediate", function setImmediate(fn: unknown, ...args: unknown[]) {
    let t: Timeout;
    const run = wrap(fn, args);
    t = new Timeout(() => { ids.delete(t._id); run(); }, 0, false);
    return t;
  });
  defineGlobal("clearTimeout", clear);
  defineGlobal("clearInterval", clear);
  defineGlobal("clearImmediate", clear);
  defineGlobal("queueMicrotask", function queueMicrotask(fn: unknown) {
    if (typeof fn !== "function") throw new TypeError("queueMicrotask: callback must be a function");
    Promise.resolve().then(() => {
      try { (fn as Function)(); } catch (e) { reportError(e); }
    });
  });
}
