import { host } from "./host";
import { defineGlobal } from "./util";
import { format, inspect } from "./inspect";

let groupIndent = "";

function emit(stream: 1 | 2, args: unknown[]) {
  let s = format(args);
  if (groupIndent) s = groupIndent + s.replace(/\n/g, "\n" + groupIndent);
  host.write(stream, s + "\n");
}

export function reportError(e: unknown) {
  // An error from a microtask (queueMicrotask, nextTick) or an EventTarget
  // listener is an uncaught exception in Node: process's listeners first
  // (the Node shims' handler, as the engine offers its own).
  const handler = (globalThis as any)[Symbol.for("og.uncaught")];
  if (typeof handler === "function" && handler(e)) return;
  emit(2, ["Uncaught", e]);
}

export function installConsole() {
  const counts = new Map<string, number>();
  const timers = new Map<string, number>();
  const console = {
    log: (...a: unknown[]) => emit(1, a),
    info: (...a: unknown[]) => emit(1, a),
    debug: (...a: unknown[]) => emit(1, a),
    warn: (...a: unknown[]) => emit(2, a),
    error: (...a: unknown[]) => emit(2, a),
    trace: (...a: unknown[]) => {
      // The engine's Error: its stack is a string whatever prepareStackTrace is.
      const NativeError = ((globalThis as any)[Symbol.for("og.nativeError")] ?? Error) as ErrorConstructor;
      const stack = (new NativeError().stack || "").split("\n").slice(2).join("\n");
      emit(2, ["Trace" + (a.length ? ": " + format(a) : "") + "\n" + stack]);
    },
    dir: (v: unknown, opts?: { depth?: number }) => host.write(1, groupIndent + inspect(v, opts) + "\n"),
    dirxml: (...a: unknown[]) => emit(1, a),
    table: (v: unknown) => emit(1, [v]),
    assert: (cond: unknown, ...a: unknown[]) => {
      if (!cond) emit(2, a.length ? ["Assertion failed:", ...a] : ["Assertion failed"]);
    },
    count: (label = "default") => {
      const n = (counts.get(label) ?? 0) + 1;
      counts.set(label, n);
      emit(1, [`${label}: ${n}`]);
    },
    countReset: (label = "default") => { counts.delete(label); },
    time: (label = "default") => { timers.set(label, host.timers.now()); },
    timeLog: (label = "default", ...a: unknown[]) => {
      const t = timers.get(label);
      if (t !== undefined) emit(1, [`${label}: ${(host.timers.now() - t).toFixed(3)}ms`, ...a]);
    },
    timeEnd: (label = "default") => {
      const t = timers.get(label);
      if (t !== undefined) { emit(1, [`${label}: ${(host.timers.now() - t).toFixed(3)}ms`]); timers.delete(label); }
    },
    group: (...a: unknown[]) => { if (a.length) emit(1, a); groupIndent += "  "; },
    groupCollapsed: (...a: unknown[]) => { if (a.length) emit(1, a); groupIndent += "  "; },
    groupEnd: () => { groupIndent = groupIndent.slice(2); },
  };
  defineGlobal("console", console);
  defineGlobal("reportError", reportError);
}
