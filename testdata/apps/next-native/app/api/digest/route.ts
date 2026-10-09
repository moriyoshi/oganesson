import { sha256, raw } from "og:hash";
import { after } from "og:clock";

export const dynamic = "force-dynamic";

export async function GET(req: Request) {
  const s = new URL(req.url).searchParams.get("s") ?? "";
  let digest: unknown;
  try {
    digest = sha256(s);
  } catch (e) {
    digest = { error: (e as Error).message };
  }
  const bytes = raw(new TextEncoder().encode(s));
  const later = await after(new Date(Date.UTC(2026, 0, 1)), 5);
  return Response.json({ digest, rawLength: bytes.length, rawIsBytes: bytes instanceof Uint8Array, later: later.toISOString() });
}
