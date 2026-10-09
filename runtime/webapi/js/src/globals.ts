import { host } from "./host";
import { defineGlobal } from "./util";
import { EventTarget } from "./events";

// Minimal process for edge-style code (process.env, nextTick). The Node
// shim layer (runtime/node) replaces this with a fuller implementation.
export function installGlobals() {
  defineGlobal("self", globalThis);
  if (typeof (globalThis as any).process === "undefined") {
    const env = host.env;
    defineGlobal("process", {
      env,
      argv: [],
      // The og process's own ids, as Node's process.pid / ppid are.
      pid: host.pid,
      ppid: host.ppid,
      platform: "linux",
      version: "v22.0.0",
      versions: {},
      browser: false,
      cwd: () => "/",
      nextTick(fn: (...a: unknown[]) => void, ...args: unknown[]) { queueMicrotask(() => fn(...args)); },
      emitWarning(w: unknown) { console.warn(w); },
      on() { return this; },
      once() { return this; },
      off() { return this; },
    });
  }

  const timeOrigin = Date.now() - host.timers.now();
  const marks = new Map<string, number>();
  const performance = Object.assign(new EventTarget(), {
    timeOrigin,
    now: () => host.timers.now(),
    mark(name: string) { const t = host.timers.now(); marks.set(name, t); return { name, entryType: "mark", startTime: t, duration: 0 }; },
    measure(name: string, start?: string, end?: string) {
      const s = start ? marks.get(start) ?? 0 : 0;
      const e = end ? marks.get(end) ?? host.timers.now() : host.timers.now();
      return { name, entryType: "measure", startTime: s, duration: e - s };
    },
    clearMarks(name?: string) { if (name) marks.delete(name); else marks.clear(); },
    clearMeasures() {},
    getEntries: () => [], getEntriesByName: () => [], getEntriesByType: () => [],
    toJSON: () => ({ timeOrigin }),
  });
  defineGlobal("performance", performance);
  defineGlobal("navigator", { userAgent: "Oganesson", language: "en-US", languages: ["en-US"], hardwareConcurrency: 1 });
}
