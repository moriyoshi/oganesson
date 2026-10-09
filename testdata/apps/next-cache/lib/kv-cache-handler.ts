import { openKv } from "og:kv";

// A "use cache" handler (cacheHandlers in next.config.ts) for the "kv"
// kind, over og:kv. Each request runs in a fresh engine, so a handler
// keeps its entries in a Go-backed store rather than module state. It
// counts its gets and sets for app/custom/stats.

interface Stored {
  value: Uint8Array;
  tags: string[];
  stale: number;
  timestamp: number;
  expire: number;
  revalidate: number;
}

async function count(what: "get" | "set") {
  const kv = await openKv();
  const n = (await kv.get<number>(["kv-cache-stats", what])).value ?? 0;
  await kv.set(["kv-cache-stats", what], n + 1);
}

async function bytes(stream: ReadableStream<Uint8Array>) {
  const chunks: Uint8Array[] = [];
  const reader = stream.getReader();
  for (let r = await reader.read(); !r.done; r = await reader.read()) chunks.push(r.value);
  const out = new Uint8Array(chunks.reduce((n, c) => n + c.byteLength, 0));
  let at = 0;
  for (const c of chunks) {
    out.set(c, at);
    at += c.byteLength;
  }
  return out;
}

const handler = {
  async get(cacheKey: string) {
    await count("get");
    const kv = await openKv();
    const e = (await kv.get<Stored>(["kv-cache", cacheKey])).value;
    if (!e || Date.now() > e.timestamp + e.revalidate * 1000) return undefined;
    const value = e.value;
    return {
      ...e,
      value: new ReadableStream<Uint8Array>({
        start(c) {
          c.enqueue(value);
          c.close();
        },
      }),
    };
  },
  async set(cacheKey: string, pending: Promise<Omit<Stored, "value"> & { value: ReadableStream<Uint8Array> }>) {
    const entry = await pending;
    const kv = await openKv();
    await kv.set(["kv-cache", cacheKey], { ...entry, value: await bytes(entry.value) });
    await count("set");
  },
  async refreshTags() {},
  async getExpiration() {
    return 0;
  },
  async updateTags() {},
};

export default handler;
