// Entry point of the runtime prelude. The bundle evaluates to install(host).
import { Host, setHost } from "./host";
import { installPolyfills } from "./polyfills";
import { installConsole } from "./console";
import { installTimers } from "./timers";
import { installEvents } from "./events";
import { installAbort } from "./abort";
import { installEncoding } from "./encoding";
import { installCompression } from "./compression";
import { installStreams } from "./streams";
import { installURL } from "./url";
import { installBlob } from "./blob";
import { installFormData } from "./formdata";
import { installHeaders } from "./headers";
import { installRequest } from "./request";
import { installResponse } from "./response";
import { installFetch } from "./fetch";
import { installCrypto } from "./crypto";
import { installStructuredClone } from "./structured-clone";
import { installMessaging } from "./messaging";
import { installGlobals } from "./globals";
import { installIntl } from "./intl";
import { installRegExp } from "./regexp";
import { defineGlobal } from "./util";
import { exports } from "./server";
import { inspect, format } from "./inspect";

export default function install(host: Host) {
  setHost(host);
  installRegExp();
  installPolyfills();
  installConsole();
  installTimers();
  installEvents();
  installAbort();
  installStreams();
  installEncoding();
  installCompression();
  installURL();
  installBlob();
  installFormData();
  installHeaders();
  installRequest();
  installResponse();
  installFetch();
  installCrypto();
  installStructuredClone();
  installMessaging();
  installGlobals();
  installIntl();
  defineGlobal("AsyncLocalStorage", host.AsyncLocalStorage);
  // Internals shared with the Node shim modules (runtime/nodeshim).
  Object.defineProperty(globalThis, Symbol.for("og.internals"), {
    value: {
      inspect, format,
      digest: host.digest, hmac: host.hmac, randomBytes: host.randomBytes, randomUUID: host.randomUUID,
      pbkdf2: host.pbkdf2, hkdf: host.hkdf, aesGcm: host.aesGcm, aesCbc: host.aesCbc, aesCtr: host.aesCtr,
    },
  });
  return exports;
}
