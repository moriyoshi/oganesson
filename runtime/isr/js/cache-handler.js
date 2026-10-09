// og:cache-handler, Next's incremental cache handler interface backed by
// Oganesson's Go-side cache: the fetch/data cache, plus tag revalidation that also
// drops cached pages (revalidatePath maps to implicit _N_T_ tags).
const host = globalThis[Symbol.for("og.cache")];

export default class OgCacheHandler {
  constructor(_ctx) {}

  async get(key, ctx) {
    if (!host) return null;
    const tags = [...((ctx && ctx.tags) || []), ...((ctx && ctx.softTags) || [])];
    const r = host.get(key, tags);
    return r ? { lastModified: r.lastModified, value: JSON.parse(r.value) } : null;
  }

  async set(key, data, ctx) {
    if (!host) return;
    if (data === null || data === undefined) {
      host.delete(key);
      return;
    }
    const tags = (ctx && ctx.tags) || (data && data.tags) || [];
    host.set(key, JSON.stringify(data), tags);
  }

  async revalidateTag(tags, _durations) {
    if (host) host.revalidateTags(Array.isArray(tags) ? tags : [tags]);
  }

  resetRequestCache() {}
}
