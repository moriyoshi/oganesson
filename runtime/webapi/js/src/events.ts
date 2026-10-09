import { defineGlobal, tag } from "./util";
import { reportError } from "./console";
import { host } from "./host";

const legacyCodes: Record<string, number> = {
  IndexSizeError: 1, HierarchyRequestError: 3, WrongDocumentError: 4, InvalidCharacterError: 5,
  NoModificationAllowedError: 7, NotFoundError: 8, NotSupportedError: 9, InvalidStateError: 11,
  SyntaxError: 12, InvalidModificationError: 13, NamespaceError: 14, InvalidAccessError: 15,
  TypeMismatchError: 17, SecurityError: 18, NetworkError: 19, AbortError: 20, URLMismatchError: 21,
  QuotaExceededError: 22, TimeoutError: 23, InvalidNodeTypeError: 24, DataCloneError: 25,
};

export class DOMException extends Error {
  #name: string;
  #message: string;
  constructor(message = "", name = "Error") {
    super(String(message));
    this.#message = String(message);
    this.#name = String(name);
    if ((Error as any).captureStackTrace) (Error as any).captureStackTrace(this, DOMException);
  }
  get name() { return this.#name; }
  get message() { return this.#message; }
  get code() { return legacyCodes[this.#name] ?? 0; }
}
tag(DOMException, "DOMException");
for (const [name, code] of Object.entries(legacyCodes)) {
  const constName = name.replace(/Error$/, "").replace(/([a-z])([A-Z])/g, "$1_$2").toUpperCase() + "_ERR";
  Object.defineProperty(DOMException, constName, { value: code, enumerable: true });
}

const NONE = 0, AT_TARGET = 2;

export class Event {
  #type: string;
  #bubbles: boolean;
  #cancelable: boolean;
  #composed: boolean;
  #defaultPrevented = false;
  #stop = false;
  #stopImmediate = false;
  #target: EventTarget | null = null;
  #current: EventTarget | null = null;
  #phase = NONE;
  #timeStamp = host.timers.now();
  #dispatching = false;
  constructor(type: string, init: { bubbles?: boolean; cancelable?: boolean; composed?: boolean } = {}) {
    if (arguments.length === 0) throw new TypeError("Event constructor requires a type");
    this.#type = String(type);
    this.#bubbles = !!init.bubbles;
    this.#cancelable = !!init.cancelable;
    this.#composed = !!init.composed;
  }
  get type() { return this.#type; }
  get bubbles() { return this.#bubbles; }
  get cancelable() { return this.#cancelable; }
  get composed() { return this.#composed; }
  get defaultPrevented() { return this.#defaultPrevented; }
  get returnValue() { return !this.#defaultPrevented; }
  set returnValue(v) { if (!v) this.preventDefault(); }
  get cancelBubble() { return this.#stop; }
  set cancelBubble(v) { if (v) this.#stop = true; }
  get target() { return this.#target; }
  get srcElement() { return this.#target; }
  get currentTarget() { return this.#current; }
  get eventPhase() { return this.#phase; }
  get timeStamp() { return this.#timeStamp; }
  get isTrusted() { return false; }
  composedPath() { return this.#dispatching && this.#target ? [this.#target] : []; }
  preventDefault() { if (this.#cancelable) this.#defaultPrevented = true; }
  stopPropagation() { this.#stop = true; }
  stopImmediatePropagation() { this.#stop = true; this.#stopImmediate = true; }
  initEvent(type: string, bubbles = false, cancelable = false) {
    if (this.#dispatching) return;
    this.#type = String(type); this.#bubbles = bubbles; this.#cancelable = cancelable;
  }
  static _begin(e: Event, target: EventTarget) {
    if (e.#dispatching) throw new DOMException("The event is already being dispatched.", "InvalidStateError");
    e.#dispatching = true; e.#target = target; e.#current = target; e.#phase = AT_TARGET;
  }
  static _end(e: Event) {
    e.#dispatching = false; e.#current = null; e.#phase = NONE; e.#stop = false; e.#stopImmediate = false;
  }
  static _stoppedImmediate(e: Event) { return e.#stopImmediate; }
}
for (const [k, v] of Object.entries({ NONE: 0, CAPTURING_PHASE: 1, AT_TARGET: 2, BUBBLING_PHASE: 3 })) {
  Object.defineProperty(Event, k, { value: v, enumerable: true });
  Object.defineProperty(Event.prototype, k, { value: v, enumerable: true });
}
tag(Event, "Event");

export class CustomEvent<T = unknown> extends Event {
  #detail: T | null;
  constructor(type: string, init: { detail?: T; bubbles?: boolean; cancelable?: boolean; composed?: boolean } = {}) {
    super(type, init);
    this.#detail = init.detail ?? null;
  }
  get detail() { return this.#detail; }
}
tag(CustomEvent, "CustomEvent");

interface Listener { callback: any; capture: boolean; once: boolean; passive: boolean; removed: boolean }

export class EventTarget {
  #listeners = new Map<string, Listener[]>();
  addEventListener(type: string, callback: any, options?: boolean | { capture?: boolean; once?: boolean; passive?: boolean; signal?: any }) {
    if (callback == null) return;
    const opts = typeof options === "object" && options !== null ? options : { capture: !!options };
    const capture = !!opts.capture;
    const signal = (opts as any).signal;
    if (signal && signal.aborted) return;
    const list = this.#listeners.get(type) ?? [];
    if (list.some((l) => l.callback === callback && l.capture === capture)) return;
    const l: Listener = { callback, capture, once: !!(opts as any).once, passive: !!(opts as any).passive, removed: false };
    list.push(l);
    this.#listeners.set(type, list);
    if (signal) signal.addEventListener("abort", () => this.removeEventListener(type, callback, { capture }));
  }
  removeEventListener(type: string, callback: any, options?: boolean | { capture?: boolean }) {
    const capture = typeof options === "object" && options !== null ? !!options.capture : !!options;
    const list = this.#listeners.get(type);
    if (!list) return;
    const i = list.findIndex((l) => l.callback === callback && l.capture === capture);
    if (i >= 0) { list[i].removed = true; list.splice(i, 1); }
  }
  dispatchEvent(event: Event) {
    if (!(event instanceof Event)) throw new TypeError("dispatchEvent: argument is not an Event");
    Event._begin(event, this);
    try {
      const list = (this.#listeners.get(event.type) ?? []).slice();
      for (const l of list) {
        if (l.removed) continue;
        if (l.once) this.removeEventListener(event.type, l.callback, { capture: l.capture });
        try {
          if (typeof l.callback === "function") l.callback.call(this, event);
          else if (l.callback && typeof l.callback.handleEvent === "function") l.callback.handleEvent(event);
        } catch (e) {
          reportError(e);
        }
        if (Event._stoppedImmediate(event)) break;
      }
      const handler = (this as any)["on" + event.type];
      if (typeof handler === "function" && !Event._stoppedImmediate(event)) {
        try { handler.call(this, event); } catch (e) { reportError(e); }
      }
    } finally {
      Event._end(event);
    }
    return !event.defaultPrevented;
  }
}
tag(EventTarget, "EventTarget");

export function installEvents() {
  defineGlobal("DOMException", DOMException);
  defineGlobal("Event", Event);
  defineGlobal("CustomEvent", CustomEvent);
  defineGlobal("EventTarget", EventTarget);
}
