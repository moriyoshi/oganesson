import { openKv, KvU64 } from "og:kv";

// Counts visits across requests. Each request runs in an isolated engine;
// the built-in store is the shared state.
export async function POST() {
  const kv = await openKv();
  await kv.atomic().sum(["visits"], BigInt(1)).commit(); // no 1n: create-next-app targets ES2017
  const { value } = await kv.get<KvU64>(["visits"]);
  return Response.json({ visits: value?.value.toString() });
}
