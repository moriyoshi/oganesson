// node:events: unenv's module with a callable EventEmitter. Node's
// EventEmitter is a plain function, and pre-class code (readable-stream and
// many other npm packages) initializes its subclasses with
// EventEmitter.call(this); unenv's is an ES class, which cannot be called.
"use strict";
const unenv = require("unenv:node/events");
const Base = unenv.EventEmitter;

function EventEmitter(opts) {
  if (new.target) return Reflect.construct(Base, [opts], new.target);
  // EventEmitter.call(this, opts): initialize this like the constructor
  // (an instance whose prototype already carries _events keeps them, as in
  // Node's EventEmitter.init).
  const init = Reflect.construct(Base, [opts], EventEmitter);
  if (this._events !== undefined && this._events !== Object.getPrototypeOf(this)._events) {
    delete init._events;
    delete init._eventsCount;
  }
  Object.assign(this, init);
}
// As in Node, on and off are the same functions as addListener and
// removeListener. unenv's call this.addListener / this.removeListener, which
// recurses forever in subclasses that alias them the other way round
// (readable-stream: Readable.prototype.addListener = Readable.prototype.on).
Base.prototype.on = Base.prototype.addListener;
Base.prototype.off = Base.prototype.removeListener;
Object.setPrototypeOf(EventEmitter, Base); // the static API
EventEmitter.prototype = Base.prototype;
for (const key of Object.keys(unenv)) {
  if (key !== "default" && key !== "EventEmitter") {
    Object.defineProperty(EventEmitter, key, { value: unenv[key], writable: true, configurable: true, enumerable: true });
  }
}
Object.defineProperty(EventEmitter, "EventEmitter", { value: EventEmitter, writable: true, configurable: true, enumerable: true });
module.exports = EventEmitter;
