import { headers } from "next/headers";

export async function GET() {
  const h = await headers();
  // The host once, though the proxy passed the request's headers on.
  return Response.json({ fromProxy: h.get("x-from-proxy"), host: h.get("host") });
}
