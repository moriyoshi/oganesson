import type { NextApiRequest, NextApiResponse } from "next";
import { openKv, KvU64 } from "og:kv";

// Counts its invocations in the built-in store (shared across requests).
export default async function handler(_req: NextApiRequest, res: NextApiResponse) {
  const kv = await openKv();
  await kv.atomic().sum(["api-count"], BigInt(1)).commit();
  const { value } = await kv.get<KvU64>(["api-count"]);
  res.json({ count: Number(value?.value ?? 0) });
}
