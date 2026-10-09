// Opts into static generation: prerendered at build time, served from the
// cache. Request data is stubbed out under force-static.
export const dynamic = "force-static";

export function GET(request: Request) {
  return Response.json({ at: Date.now(), ua: request.headers.get("user-agent") }, { headers: { "x-route": "build-time" } });
}
