// node:_http_common, the internal module behind Node's HTTP client and
// server that tools reach for (msw's interceptors parse raw HTTP with its
// HTTPParser). HTTPParser is http-parser-js (vendored; the API of Node's
// http_parser binding: initialize, execute, finish, free, kOn* callbacks,
// method indexes into methods), with the callback slots Node also has.
"use strict";
const { HTTPParser, methods } = require("/http-parser-js/http-parser.js");

// Reading kOnExecute takes http-parser-js out of its Node 0.12 mode, where
// kOnHeadersComplete gets one info object instead of Node's arguments; its
// value stays the library's (99, a slot key; Node's is 5).
void HTTPParser.kOnExecute;
HTTPParser.kOnMessageBegin = 0;
HTTPParser.kOnTimeout = 6;
if (typeof HTTPParser.prototype.close !== "function") HTTPParser.prototype.close = function () {};
if (typeof HTTPParser.prototype.remove !== "function") HTTPParser.prototype.remove = function () {};

const tokenRegExp = /^[\^_`a-zA-Z\-0-9!#$%&'*+.|~]+$/;
const headerCharRegex = /[^\t\x20-\x7e\x80-\xff]/;

// Node keeps idle parsers in a FreeList; a new one is as good.
const parsers = {
  name: "parsers",
  max: 1000,
  list: [],
  alloc() {
    return this.list.length > 0 ? this.list.pop() : new HTTPParser();
  },
  free(parser) {
    if (this.list.length < this.max) {
      this.list.push(parser);
      return true;
    }
    return false;
  },
};

function freeParser(parser, req, socket) {
  if (!parser) return;
  for (const k of [0, 1, 2, 3, 4, HTTPParser.kOnExecute, 6]) parser[k] = null;
  if (typeof parser.free === "function") parser.free();
  parser.socket = null;
  parser.incoming = null;
  parser.outgoing = null;
  if (req) req.parser = null;
  if (socket) socket.parser = null;
}

function prepareError(err, parser, rawPacket) {
  err.rawPacket = rawPacket || (parser && parser.getCurrentBuffer ? parser.getCurrentBuffer() : undefined);
  if (typeof err.reason === "string") err.message = `Parse Error: ${err.reason}`;
}

module.exports = {
  _checkInvalidHeaderChar: (val) => headerCharRegex.test(val),
  _checkIsHttpToken: (val) => tokenRegExp.test(val),
  chunkExpression: /(?:^|\W)chunked(?:$|\W)/i,
  continueExpression: /(?:^|\W)100-continue(?:$|\W)/i,
  CRLF: "\r\n",
  freeParser,
  methods,
  parsers,
  kIncomingMessage: Symbol("IncomingMessage"),
  HTTPParser,
  isLenient: () => false,
  prepareError,
};
