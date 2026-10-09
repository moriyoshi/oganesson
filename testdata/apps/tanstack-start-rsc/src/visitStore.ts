import { openKv } from "og:kv";

// The visit count lives in og:kv, which outlives the per-request VM. Only
// server code imports this module.
export async function readVisits(): Promise<number> {
  const kv = await openKv();
  return (await kv.get<number>(["visits"])).value ?? 0;
}

export async function bump(): Promise<number> {
  const kv = await openKv();
  const next = (await readVisits()) + 1;
  await kv.set(["visits"], next);
  return next;
}
