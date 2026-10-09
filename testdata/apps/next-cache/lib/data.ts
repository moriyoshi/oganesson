import { cacheLife, cacheTag } from "next/cache";

let calls = 0;

// A cached function: its result (and the counter's value inside it) is
// shared by every request until it expires.
export async function stamp(label: string) {
  "use cache";
  cacheLife("hours");
  calls++;
  return `${label} computed at ${Date.now()} (call ${calls})`;
}

// Tagged, so a server action can invalidate it.
export async function tagged() {
  "use cache";
  cacheTag("og-tagged");
  return `tagged ${Math.random().toString(36).slice(2)}`;
}

// Cached by the custom "kv" handler (lib/kv-cache-handler.ts).
export async function kvStamp() {
  "use cache: kv";
  cacheLife("hours");
  return `kv ${Math.random().toString(36).slice(2)}`;
}

// The next.config profile "brief": recomputed a second after it is cached.
export async function briefStamp() {
  "use cache";
  cacheLife("brief");
  return `brief ${Math.random().toString(36).slice(2)}`;
}
