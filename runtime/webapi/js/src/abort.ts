import { DOMException, Event, EventTarget } from "./events";
import { defineGlobal, tag } from "./util";

const kCreate = Symbol("create");

export class AbortSignal extends EventTarget {
  #aborted = false;
  #reason: unknown = undefined;
  #algorithms = new Set<() => void>();
  onabort: ((e: Event) => void) | null = null;

  constructor(key?: symbol) {
    if (key !== kCreate) throw new TypeError("Illegal constructor");
    super();
  }
  get aborted() { return this.#aborted; }
  get reason() { return this.#reason; }
  throwIfAborted() { if (this.#aborted) throw this.#reason; }

  // _follow runs fn once when the signal aborts (spec "add an algorithm").
  _follow(fn: () => void) { this.#algorithms.add(fn); return () => this.#algorithms.delete(fn); }
  _abort(reason: unknown) {
    if (this.#aborted) return;
    this.#aborted = true;
    this.#reason = reason === undefined ? new DOMException("This operation was aborted", "AbortError") : reason;
    for (const fn of this.#algorithms) fn();
    this.#algorithms.clear();
    this.dispatchEvent(new Event("abort"));
  }

  static abort(reason?: unknown) {
    const s = new AbortSignal(kCreate);
    s._abort(reason);
    return s;
  }
  static timeout(ms: number) {
    const s = new AbortSignal(kCreate);
    const t = setTimeout(() => s._abort(new DOMException("The operation was aborted due to timeout", "TimeoutError")), ms) as any;
    t?.unref?.();
    return s;
  }
  static any(signals: Iterable<AbortSignal>) {
    const s = new AbortSignal(kCreate);
    const list = [...signals];
    for (const x of list) {
      if (x.aborted) { s._abort(x.reason); return s; }
    }
    for (const x of list) x._follow(() => s._abort(x.reason));
    return s;
  }
}
tag(AbortSignal, "AbortSignal");

export class AbortController {
  #signal = new AbortSignal(kCreate);
  get signal() { return this.#signal; }
  abort(reason?: unknown) { this.#signal._abort(reason); }
}
tag(AbortController, "AbortController");

export function newAbortSignal() { return new AbortSignal(kCreate); }

export function installAbort() {
  defineGlobal("AbortSignal", AbortSignal);
  defineGlobal("AbortController", AbortController);
}
