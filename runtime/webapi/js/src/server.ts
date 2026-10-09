// Helpers Go uses to bridge net/http and the JS Request/Response classes.
import { Request } from "./request";
import { Response } from "./response";
import { Headers } from "./headers";
import { AbortController } from "./abort";
import { networkBody } from "./fetch";
import { utf8Encode } from "./encoding";
import { toBytes, isBufferSource } from "./util";

export const exports = {
  incomingRequest(spec: { method: string; url: string; headers: [string, string][]; bodyId: number }) {
    const ac = new AbortController();
    const init: any = { method: spec.method, headers: spec.headers, signal: ac.signal };
    if (spec.bodyId) {
      init.body = networkBody(spec.bodyId, ac.signal);
      init.duplex = "half";
    }
    return { request: new Request(spec.url, init), abort: (reason?: unknown) => ac.abort(reason) };
  },

  responseInfo(resp: unknown) {
    if (!(resp instanceof Response)) {
      throw new TypeError(`The handler must return a Response, got ${resp === null ? "null" : typeof resp}`);
    }
    let next: (() => Promise<Uint8Array | null>) | null = null;
    let cancel: ((reason?: unknown) => void) | null = null;
    const body = resp.body;
    if (body) {
      const reader = body.getReader();
      next = async () => {
        const { done, value } = await reader.read();
        if (done) return null;
        if (typeof value === "string") return utf8Encode(value);
        if (!isBufferSource(value)) throw new TypeError("Response body chunks must be Uint8Array");
        return new Uint8Array(toBytes(value as any));
      };
      cancel = (reason?: unknown) => { reader.cancel(reason).catch(() => {}); };
    }
    return {
      type: resp.type,
      status: resp.status,
      statusText: resp.statusText,
      headers: Headers._list(resp.headers).slice(),
      next,
      cancel,
    };
  },
};
