import { host } from "./host";
import { Headers } from "./headers";
import { Request } from "./request";
import { Response, nullBodyStatus } from "./response";
import { URL } from "./url";
import { Blob } from "./blob";
import { bodyState, consume, streamFromBytes } from "./body";
import { defineGlobal } from "./util";
import { utf8Encode } from "./encoding";
import { parseMIMEType, serializeMIMEType } from "./mime";

const redirectStatus = new Set([301, 302, 303, 307, 308]);

// The Fetch Standard's "bad port" list.
const badPorts = new Set([0, 1, 7, 9, 11, 13, 15, 17, 19, 20, 21, 22, 23, 25, 37, 42, 43, 53, 69, 77, 79, 87, 95, 101, 102, 103, 104, 109, 110, 111, 113, 115, 117, 119, 123, 135, 137, 139, 143, 161, 179, 389, 427, 465, 512, 513, 514, 515, 526, 530, 531, 532, 540, 548, 554, 556, 563, 587, 601, 636, 989, 990, 993, 995, 1719, 1720, 1723, 2049, 3659, 4045, 4190, 5060, 5061, 6000, 6566, 6665, 6666, 6667, 6668, 6669, 6679, 6697, 10080]);

function checkPort(url: URL) {
  const port = url.port === "" ? (url.protocol === "https:" ? 443 : 80) : Number(url.port);
  if (badPorts.has(port)) throw new TypeError(`fetch failed: port ${port} is blocked`);
}
const MAX_REDIRECTS = 20;

// dataURL implements the Fetch Standard's data: URL processor.
function dataURL(url: URL): Response {
  const href = url.href.replace(/#.*$/s, "");
  const input = href.slice("data:".length);
  const comma = input.indexOf(",");
  if (comma < 0) throw new TypeError("fetch failed: invalid data: URL");
  let mimeType = input.slice(0, comma).replace(/^[\t\n\f\r ]+|[\t\n\f\r ]+$/g, "");
  let body = decodeURIComponentBytes(input.slice(comma + 1));
  const b64 = /;[\x20]*base64$/i.exec(mimeType);
  if (b64) {
    let iso = "";
    for (let i = 0; i < body.length; i++) iso += String.fromCharCode(body[i]);
    let bin: string;
    try { bin = atob(iso); } catch { throw new TypeError("fetch failed: invalid base64 in data: URL"); }
    body = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) body[i] = bin.charCodeAt(i);
    mimeType = mimeType.slice(0, b64.index);
  }
  if (mimeType.startsWith(";")) mimeType = "text/plain" + mimeType;
  const parsed = parseMIMEType(mimeType);
  const type = parsed ? serializeMIMEType(parsed) : "text/plain;charset=US-ASCII";
  return Response._network({ status: 200, statusText: "OK", headers: [["content-type", type]], url: url.href, redirected: false, stream: streamFromBytes(body) });
}

function decodeURIComponentBytes(s: string): Uint8Array {
  const src = utf8Encode(s);
  const out: number[] = [];
  for (let i = 0; i < src.length; i++) {
    if (src[i] === 0x25 && i + 2 < src.length) {
      const h = String.fromCharCode(src[i + 1], src[i + 2]);
      if (/^[0-9A-Fa-f]{2}$/.test(h)) { out.push(parseInt(h, 16)); i += 2; continue; }
    }
    out.push(src[i]);
  }
  return new Uint8Array(out);
}

export function networkBody(bodyId: number, signal: AbortSignal): ReadableStream<Uint8Array> {
  let closed = false;
  const close = () => { if (!closed) { closed = true; host.bodyClose(bodyId); } };
  let controllerRef: ReadableStreamDefaultController<Uint8Array>;
  const onAbort = () => { close(); try { controllerRef.error(signal.reason); } catch { /* already closed */ } };
  return new ReadableStream<Uint8Array>({
    start(c) {
      controllerRef = c;
      if (signal.aborted) onAbort(); else signal.addEventListener("abort", onAbort, { once: true });
    },
    async pull(c) {
      const chunk = await host.bodyRead(bodyId);
      if (chunk === null) {
        closed = true;
        signal.removeEventListener("abort", onAbort);
        c.close();
      } else {
        c.enqueue(new Uint8Array(chunk));
      }
    },
    cancel() { signal.removeEventListener("abort", onAbort); close(); },
  });
}

export async function fetch(input: unknown, init?: any): Promise<Response> {
  const request = new Request(input, init);
  const signal = request.signal;
  if (signal.aborted) throw signal.reason;

  let url = new URL(request.url);
  if (url.protocol === "data:") return dataURL(url);
  if (url.protocol === "blob:") {
    const obj = URL._resolveBlob(url.href);
    if (!(obj instanceof Blob) || request.method !== "GET") throw new TypeError("fetch failed: invalid blob URL");
    return Response._network({ status: 200, statusText: "OK", headers: [["content-type", obj.type], ["content-length", String(obj.size)]], url: url.href, redirected: false, stream: obj.stream() });
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") throw new TypeError(`fetch failed: unsupported scheme ${url.protocol}`);
  checkPort(url);

  let method = request.method;
  const headers = new Headers(request.headers);
  if (!headers.has("accept")) headers.set("accept", "*/*");
  let body: Uint8Array | null = bodyState(request).body ? await consume(request) : null;

  for (let redirects = 0; ; redirects++) {
    if (signal.aborted) throw signal.reason;
    const [id, pending] = host.fetchStart({ method, url: url.href, headers: [...Headers._list(headers)], body });
    const onAbort = () => host.fetchAbort(id);
    signal.addEventListener("abort", onAbort, { once: true });
    let res;
    try {
      res = await pending;
    } catch (e) {
      if (signal.aborted) throw signal.reason;
      throw e;
    } finally {
      signal.removeEventListener("abort", onAbort);
    }

    const location = res.headers.find(([k]) => k.toLowerCase() === "location")?.[1];
    if (redirectStatus.has(res.status) && location !== undefined && request.redirect !== "manual") {
      host.bodyClose(res.bodyId);
      if (request.redirect === "error") throw new TypeError("fetch failed: unexpected redirect");
      if (redirects >= MAX_REDIRECTS) throw new TypeError("fetch failed: redirect count exceeded");
      let next: URL;
      try { next = new URL(location, url.href); } catch { throw new TypeError(`fetch failed: invalid redirect location ${location}`); }
      if (next.protocol !== "http:" && next.protocol !== "https:") throw new TypeError("fetch failed: redirect to non-HTTP(S) URL");
      checkPort(next);
      if (((res.status === 301 || res.status === 302) && method === "POST") || (res.status === 303 && method !== "GET" && method !== "HEAD")) {
        method = "GET";
        body = null;
        for (const h of ["content-encoding", "content-language", "content-location", "content-type", "content-length"]) headers.delete(h);
      }
      if (next.origin !== url.origin) headers.delete("authorization");
      url = next;
      continue;
    }

    const hasBody = method !== "HEAD" && !nullBodyStatus.has(res.status);
    if (!hasBody) host.bodyClose(res.bodyId);
    return Response._network({
      status: res.status,
      statusText: res.statusText,
      headers: res.headers.map(([k, v]) => [k.toLowerCase(), v] as [string, string]),
      url: url.href,
      redirected: redirects > 0,
      stream: hasBody ? networkBody(res.bodyId, signal) : null,
    });
  }
}

export function installFetch() {
  defineGlobal("fetch", function fetchWrapper(input: unknown, init?: unknown) {
    // Keep fetch's own rejections asynchronous, as required.
    return fetch(input, init);
  });
}
