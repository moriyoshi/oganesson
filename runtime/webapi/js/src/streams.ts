import * as ponyfill from "../vendor/web-streams-polyfill/ponyfill.mjs";
import { defineGlobal } from "./util";

export function installStreams() {
  for (const [name, value] of Object.entries(ponyfill)) {
    if (typeof (globalThis as any)[name] === "undefined") defineGlobal(name, value);
  }
  const RS = (globalThis as any).ReadableStream;
  if (!RS.prototype[Symbol.asyncIterator] && RS.prototype.values) {
    Object.defineProperty(RS.prototype, Symbol.asyncIterator, { value: RS.prototype.values, writable: true, configurable: true });
  }
}
