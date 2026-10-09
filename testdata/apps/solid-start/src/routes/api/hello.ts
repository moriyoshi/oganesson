import type { APIEvent } from "@solidjs/start/server";

export function GET({ request }: APIEvent) {
  return Response.json({ hello: new URL(request.url).searchParams.get("name") ?? "world" });
}

export async function POST({ request }: APIEvent) {
  return Response.json({ echoed: await request.json() });
}
