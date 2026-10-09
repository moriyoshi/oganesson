// node:tls: unenv's module with a client TLSSocket over Go's crypto/tls
// (runtime/nodeshim/net.go). tls.connect either opens its own connection
// (handshake before any data) or upgrades a connected net.Socket in place
// ({ socket }, as Postgres and MySQL drivers do after their SSL request).
// As in Node, the chain is verified natively (verifyError, OpenSSL's codes)
// and the host's identity in JS by options.checkServerIdentity, Node's
// tls.checkServerIdentity by default.
"use strict";
const base = require("unenv:node/tls");
const net = require("net");
const { Buffer } = require("buffer");

const { kId, kTLS, host, systemError } = net[Symbol.for("og.net.internals")];

// OpenSSL's messages for the verify codes net.go reports.
const verifyMessages = {
  DEPTH_ZERO_SELF_SIGNED_CERT: "self-signed certificate",
  SELF_SIGNED_CERT_IN_CHAIN: "self-signed certificate in certificate chain",
  UNABLE_TO_VERIFY_LEAF_SIGNATURE: "unable to verify the first certificate",
  UNABLE_TO_GET_ISSUER_CERT_LOCALLY: "unable to get local issuer certificate",
  UNABLE_TO_GET_ISSUER_CERT: "unable to get issuer certificate",
  CERT_HAS_EXPIRED: "certificate has expired",
  CERT_NOT_YET_VALID: "certificate is not yet valid",
};

// checkServerIdentity, unfqdn, splitHost and check follow Node's lib/tls.js
// (v24.15.0).
function unfqdn(host) {
  return host.replace(/[.]$/, "");
}
function splitHost(host) {
  return unfqdn(host).replace(/[A-Z]/g, (c) => String.fromCharCode(32 + c.charCodeAt(0))).split(".");
}
function check(hostParts, pattern, wildcards) {
  if (!pattern) return false;
  const patternParts = splitHost(pattern);
  if (hostParts.length !== patternParts.length) return false;
  if (patternParts.includes("")) return false;
  const isBad = (s) => /[^!-\u007F]/u.test(s);
  if (patternParts.some(isBad)) return false;
  for (let i = hostParts.length - 1; i > 0; i -= 1) {
    if (hostParts[i] !== patternParts[i]) return false;
  }
  const hostSubdomain = hostParts[0];
  const patternSubdomain = patternParts[0];
  const patternSubdomainParts = patternSubdomain.split("*", 3);
  if (patternSubdomainParts.length === 1 || patternSubdomain.includes("xn--")) return hostSubdomain === patternSubdomain;
  if (!wildcards) return false;
  if (patternSubdomainParts.length > 2) return false;
  if (patternParts.length <= 2) return false;
  const [prefix, suffix] = patternSubdomainParts;
  if (prefix.length + suffix.length > hostSubdomain.length) return false;
  if (!hostSubdomain.startsWith(prefix)) return false;
  if (!hostSubdomain.endsWith(suffix)) return false;
  return true;
}
function splitEscapedAltNames(altNames) {
  const result = [];
  let currentToken = "";
  let offset = 0;
  while (offset !== altNames.length) {
    const nextSep = altNames.indexOf(",", offset);
    const nextQuote = altNames.indexOf('"', offset);
    if (nextQuote !== -1 && (nextSep === -1 || nextQuote < nextSep)) {
      currentToken += altNames.substring(offset, nextQuote);
      const match = /^"((?:[^"\\]|\\.)*)"/.exec(altNames.substring(nextQuote));
      if (!match) throw Object.assign(new Error("Invalid subject alternative name string"), { code: "ERR_TLS_CERT_ALTNAME_FORMAT" });
      currentToken += JSON.parse(match[0]);
      offset = nextQuote + match[0].length;
    } else if (nextSep !== -1) {
      currentToken += altNames.substring(offset, nextSep);
      result.push(currentToken);
      currentToken = "";
      offset = nextSep + 2;
    } else {
      currentToken += altNames.substring(offset);
      offset = altNames.length;
    }
  }
  result.push(currentToken);
  return result;
}
function canonicalizeIP(ip) {
  return host().canonicalizeIP(ip) ?? ip;
}
function checkServerIdentity(hostname, cert) {
  const subject = cert.subject;
  const altNames = cert.subjectaltname;
  const dnsNames = [];
  const ips = [];
  hostname = "" + hostname;
  if (altNames) {
    const splitAltNames = altNames.includes('"') ? splitEscapedAltNames(altNames) : altNames.split(", ");
    for (const name of splitAltNames) {
      if (name.startsWith("DNS:")) dnsNames.push(name.slice(4));
      else if (name.startsWith("IP Address:")) ips.push(canonicalizeIP(name.slice(11)));
    }
  }
  let valid = false;
  let reason = "Unknown reason";
  hostname = unfqdn(hostname);
  if (net.isIP(hostname)) {
    valid = ips.includes(canonicalizeIP(hostname));
    if (!valid) reason = `IP: ${hostname} is not in the cert's list: ` + ips.join(", ");
  } else if (dnsNames.length > 0 || subject?.CN) {
    const hostParts = splitHost(hostname);
    const wildcard = (pattern) => check(hostParts, pattern, true);
    if (dnsNames.length > 0) {
      valid = dnsNames.some(wildcard);
      if (!valid) reason = `Host: ${hostname}. is not in the cert's altnames: ${altNames}`;
    } else {
      const cn = subject.CN;
      if (Array.isArray(cn)) valid = cn.some(wildcard);
      else if (cn) valid = wildcard(cn);
      if (!valid) reason = `Host: ${hostname}. is not cert's CN: ${cn}`;
    }
  } else {
    reason = "Cert does not contain a DNS name";
  }
  if (!valid) {
    const err = new Error(`Hostname/IP does not match certificate's altnames: ${reason}`);
    err.code = "ERR_TLS_CERT_ALTNAME_INVALID";
    err.reason = reason;
    err.host = hostname;
    err.cert = cert;
    return err;
  }
}

const pem = (v) => (v == null ? [] : (Array.isArray(v) ? v : [v]).map((x) => (Buffer.isBuffer(x) || x instanceof Uint8Array ? Buffer.from(x).toString("utf8") : String(x))));

// What net.go's tlsSetup reads from tls.connect's options.
function goOptions(options) {
  const o = { ca: pem(options.ca), minVersion: options.minVersion, maxVersion: options.maxVersion };
  if (options.servername) o.servername = String(options.servername);
  if (options.cert && options.key) {
    o.cert = pem(options.cert).join("\n");
    o.key = pem(options.key).join("\n");
  }
  if (options.ALPNProtocols) o.ALPNProtocols = Array.from(options.ALPNProtocols, (p) => (typeof p === "string" ? p : Buffer.from(p).toString()));
  return o;
}

class SecureContext {
  constructor(options = {}) {
    this.context = { options };
  }
}
function createSecureContext(options = {}) {
  return new SecureContext(options);
}

// JSStreamSocket: Node's net.Socket over a JS stream (internal), as far as
// code can reach it.
class JSStreamSocket extends net.Socket {
  constructor(stream) {
    super();
    this.stream = stream;
  }
}

class TLSSocket extends net.Socket {
  constructor(socket, options = {}) {
    super({ allowHalfOpen: socket?.allowHalfOpen ?? options.allowHalfOpen });
    this.encrypted = true;
    this.authorized = false;
    this.authorizationError = null;
    this.alpnProtocol = false;
    this.servername = options.servername ?? null;
    this.secureConnecting = true;
    this._secure = null;
    this._parent = socket instanceof net.Socket ? socket : null;
    // Any other stream: Node wraps it in its JSStreamSocket, which code
    // reaches through _handle._parentWrap (http2-wrapper takes the class
    // from there when it loads). og terminates TLS in Go on a connection,
    // so such a socket cannot handshake (connect refuses it).
    if (socket && !this._parent) this._handle = { _parentWrap: new JSStreamSocket(socket) };
  }
  _event(type, a) {
    if (type !== "secure") return super._event(type, a);
    this._secure = a;
    this.alpnProtocol = a.alpnProtocol || false;
    this.secureConnecting = false;
    this.emit("secure");
  }
  _write(chunk, encoding, cb) {
    if (this.secureConnecting) return this.once("secureConnect", () => super._write(chunk, encoding, cb));
    super._write(chunk, encoding, cb);
  }
  _final(cb) {
    if (this.secureConnecting) return this.once("secureConnect", () => super._final(cb));
    super._final(cb);
  }
  // The socket it upgraded closes with it, first (as Node's).
  _destroy(err, cb) {
    if (this._parent && !this._parent.destroyed) this._parent.destroy();
    super._destroy(err, cb);
  }
  getPeerCertificate(detailed) {
    const c = this._secure?.peerCertificate;
    if (!c) return this._secure ? {} : null;
    const cert = { ...c, raw: Buffer.from(c.raw) };
    if (detailed) cert.issuerCertificate = cert;
    return cert;
  }
  getProtocol() {
    return this._secure ? this._secure.protocol : null;
  }
  getCipher() {
    return this._secure ? { name: this._secure.cipher, standardName: this._secure.cipher, version: this._secure.protocol } : null;
  }
  getSession() {
    return undefined;
  }
  isSessionReused() {
    return false;
  }
  setServername(name) {
    this.servername = name;
  }
  getEphemeralKeyInfo() {
    return null;
  }
}

function normalizeConnectArgs(listArgs) {
  const [options, cb] = net._normalizeArgs(listArgs);
  if (listArgs[1] !== null && typeof listArgs[1] === "object") Object.assign(options, listArgs[1]);
  else if (listArgs[2] !== null && typeof listArgs[2] === "object") Object.assign(options, listArgs[2]);
  return [options, cb];
}

// After the handshake: Node's onConnectSecure (the chain's verdict, then
// checkServerIdentity), "secureConnect", or the error.
function onConnectSecure(options) {
  const sock = this;
  const s = sock._secure;
  let verifyError = null;
  if (s.verifyError) {
    // Node adds this hint to these three reasons (src/crypto/crypto_tls.cc).
    const hint = ["UNABLE_TO_VERIFY_LEAF_SIGNATURE", "DEPTH_ZERO_SELF_SIGNED_CERT", "UNABLE_TO_GET_ISSUER_CERT"].includes(s.verifyError)
      ? "; if the root CA is installed locally, try running Node.js with --use-system-ca"
      : "";
    verifyError = new Error((verifyMessages[s.verifyError] || s.verifyError) + hint);
    verifyError.code = s.verifyError;
  } else {
    const hostname = options.servername || options.host || options.socket?._host || "localhost";
    verifyError = options.checkServerIdentity(hostname, sock.getPeerCertificate(true)) || null;
  }
  if (verifyError) {
    sock.authorized = false;
    sock.authorizationError = verifyError.code || verifyError.message;
    if (options.rejectUnauthorized) {
      sock.destroy(verifyError);
      return;
    }
  } else {
    sock.authorized = true;
  }
  sock.emit("secureConnect");
}

function connect(...args) {
  let [options, cb] = normalizeConnectArgs(args);
  const allowUnauthorized = process.env.NODE_TLS_REJECT_UNAUTHORIZED === "0";
  options = { rejectUnauthorized: !allowUnauthorized, checkServerIdentity, ...options };
  if (options.secureContext?.context?.options) options = { ...options.secureContext.context.options, ...options };
  if (typeof options.checkServerIdentity !== "function") throw new TypeError('The "options.checkServerIdentity" property must be of type function');
  if (options.socket && !(options.socket instanceof net.Socket)) throw new Error("og: tls.connect({ socket }) needs a net.Socket");
  const tlssock = new TLSSocket(options.socket, options);
  if (cb) tlssock.once("secureConnect", cb);
  tlssock.once("secure", () => onConnectSecure.call(tlssock, options));
  const go = goOptions(options);
  if (!options.socket) {
    if (options.timeout) tlssock.setTimeout(options.timeout);
    tlssock.connect([{ ...options, [kTLS]: go }, null]);
    return tlssock;
  }
  // Upgrade the given socket's connection: it hands its connection over and
  // reads no more.
  const parent = options.socket;
  const start = () => {
    if (parent.destroyed || parent[kId] === null) {
      tlssock.destroy(Object.assign(new Error("Client network socket disconnected before secure TLS connection was established"), { code: "ECONNRESET" }));
      return;
    }
    tlssock[kId] = parent[kId];
    tlssock._info = parent._info;
    tlssock._host = parent._host;
    parent[kId] = null;
    host().startTLS(tlssock[kId], go, (type, a) => tlssock._event(type, a));
  };
  if (parent.connecting) parent.once("connect", start);
  else process.nextTick(start);
  return tlssock;
}

module.exports = Object.assign({}, base, {
  TLSSocket,
  SecureContext,
  connect,
  createSecureContext,
  checkServerIdentity,
  DEFAULT_MIN_VERSION: "TLSv1.2",
  DEFAULT_MAX_VERSION: "TLSv1.3",
  // The embedded Mozilla roots (net.go), where unenv's base leaves this empty.
  rootCertificates: host().rootCertificates || base.rootCertificates,
});
