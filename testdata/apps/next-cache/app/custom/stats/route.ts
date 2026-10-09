import { connection } from "next/server";
import { openKv } from "og:kv";

// The custom handler's counters (lib/kv-cache-handler.ts).
export async function GET() {
  await connection();
  const kv = await openKv();
  const [get, set] = await kv.getMany<[number, number]>([
    ["kv-cache-stats", "get"],
    ["kv-cache-stats", "set"],
  ]);
  return Response.json({ get: get.value ?? 0, set: set.value ?? 0 });
}
