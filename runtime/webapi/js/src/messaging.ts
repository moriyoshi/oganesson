// MessageEvent, MessageChannel / MessagePort and BroadcastChannel, which
// Node has as globals (undici's webidl reads MessagePort when it loads).
// Within one VM: a message is structured-cloned when posted and delivered
// on a later task, as Node delivers port messages. Node's port extensions
// (on / once / off, ref / unref / hasRef) are there; the ref methods do
// nothing, since a request's VM ends with its response.
import { DOMException, Event, EventTarget } from "./events";
import { structuredClone } from "./structured-clone";
import { defineGlobal } from "./util";

type MessageEventInit = { data?: unknown; origin?: string; lastEventId?: string; source?: unknown; ports?: MessagePort[]; bubbles?: boolean; cancelable?: boolean };

export class MessageEvent extends Event {
  #data: unknown;
  #origin: string;
  #lastEventId: string;
  #source: unknown;
  #ports: MessagePort[];
  constructor(type: string, init: MessageEventInit = {}) {
    super(type, init);
    this.#data = init.data === undefined ? null : init.data;
    this.#origin = init.origin === undefined ? "" : String(init.origin);
    this.#lastEventId = init.lastEventId === undefined ? "" : String(init.lastEventId);
    this.#source = init.source === undefined ? null : init.source;
    this.#ports = Object.freeze([...(init.ports ?? [])]) as MessagePort[];
  }
  get data() { return this.#data; }
  get origin() { return this.#origin; }
  get lastEventId() { return this.#lastEventId; }
  get source() { return this.#source; }
  get ports() { return this.#ports; }
}

// later runs fn on a later task (a timer, as Node's port delivery is not a
// microtask).
const later = (fn: () => void) => (globalThis as any).setTimeout(fn, 0);

// nodeListeners maps a Node-style listener to the EventTarget listener that
// passes it the data (Node's port.on("message", (data) => …)).
const nodeListeners = new WeakMap<Function, EventListener>();

export class MessagePort extends EventTarget {
  #other: MessagePort | null = null;
  #queue: unknown[] = [];
  #started = false;
  #closed = false;
  #onmessage: ((e: MessageEvent) => void) | null = null;
  #onmessageerror: ((e: MessageEvent) => void) | null = null;

  static _entangle(a: MessagePort, b: MessagePort) {
    a.#other = b;
    b.#other = a;
  }

  postMessage(message: unknown, transfer?: unknown) {
    const data = structuredClone(message, Array.isArray(transfer) ? { transfer } : (transfer as any));
    const other = this.#other;
    if (this.#closed || !other || other.#closed) return;
    other.#queue.push(data);
    other.#flush();
  }
  start() {
    if (this.#started) return;
    this.#started = true;
    this.#flush();
  }
  close() {
    if (this.#closed) return;
    this.#closed = true;
    const other = this.#other;
    this.#other = null;
    later(() => this.dispatchEvent(new Event("close")));
    if (other && !other.#closed) other.close();
  }
  #flush() {
    if (!this.#started || this.#queue.length === 0) return;
    later(() => {
      while (this.#started && !this.#closed && this.#queue.length > 0) {
        const data = this.#queue.shift();
        this.dispatchEvent(new MessageEvent("message", { data }));
      }
    });
  }
  get onmessage() { return this.#onmessage; }
  // (EventTarget's dispatch calls on<type> itself.)
  set onmessage(fn) {
    this.#onmessage = typeof fn === "function" ? fn : null;
    if (this.#onmessage) this.start();
  }
  get onmessageerror() { return this.#onmessageerror; }
  set onmessageerror(fn) {
    this.#onmessageerror = typeof fn === "function" ? fn : null;
  }
  // In Node, adding a "message" listener starts the port (in browsers only
  // onmessage does).
  addEventListener(type: string, listener: any, options?: any) {
    super.addEventListener(type, listener, options);
    if (type === "message") this.start();
  }
  // Node's NodeEventTarget surface: a "message" listener gets the data
  // and starts the port.
  on(type: string, fn: (v: unknown) => void) {
    const l: EventListener = (e: any) => fn(type === "message" || type === "messageerror" ? e.data : e);
    nodeListeners.set(fn, l);
    this.addEventListener(type, l);
    if (type === "message") this.start();
    return this;
  }
  addListener(type: string, fn: (v: unknown) => void) { return this.on(type, fn); }
  once(type: string, fn: (v: unknown) => void) {
    const l: EventListener = (e: any) => fn(type === "message" || type === "messageerror" ? e.data : e);
    nodeListeners.set(fn, l);
    this.addEventListener(type, l, { once: true });
    if (type === "message") this.start();
    return this;
  }
  off(type: string, fn: Function) {
    const l = nodeListeners.get(fn);
    if (l) this.removeEventListener(type, l);
    return this;
  }
  removeListener(type: string, fn: Function) { return this.off(type, fn); }
  ref() { return this; }
  unref() { return this; }
  hasRef() { return false; }
}

export class MessageChannel {
  readonly port1: MessagePort;
  readonly port2: MessagePort;
  constructor() {
    this.port1 = new MessagePort();
    this.port2 = new MessagePort();
    MessagePort._entangle(this.port1, this.port2);
  }
}

// BroadcastChannel: every other open channel of the same name in this VM
// gets the message.
const channels = new Map<string, Set<BroadcastChannel>>();

export class BroadcastChannel extends EventTarget {
  #name: string;
  #closed = false;
  #onmessage: ((e: MessageEvent) => void) | null = null;
  constructor(name: string) {
    super();
    if (arguments.length === 0) throw new TypeError("BroadcastChannel constructor requires a name");
    this.#name = String(name);
    if (!channels.has(this.#name)) channels.set(this.#name, new Set());
    channels.get(this.#name)!.add(this);
  }
  get name() { return this.#name; }
  postMessage(message: unknown) {
    if (this.#closed) throw new DOMException("BroadcastChannel is closed.", "InvalidStateError");
    const data = structuredClone(message);
    for (const ch of channels.get(this.#name) ?? []) {
      if (ch === this) continue;
      later(() => {
        if (!ch.#closed) ch.dispatchEvent(new MessageEvent("message", { data }));
      });
    }
  }
  close() {
    this.#closed = true;
    channels.get(this.#name)?.delete(this);
  }
  get onmessage() { return this.#onmessage; }
  set onmessage(fn) {
    this.#onmessage = typeof fn === "function" ? fn : null;
  }
  ref() { return this; }
  unref() { return this; }
}

export function installMessaging() {
  defineGlobal("MessageEvent", MessageEvent);
  defineGlobal("MessagePort", MessagePort);
  defineGlobal("MessageChannel", MessageChannel);
  defineGlobal("BroadcastChannel", BroadcastChannel);
}
