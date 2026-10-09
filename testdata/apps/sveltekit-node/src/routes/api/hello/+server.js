import { json } from "@sveltejs/kit";

export function GET({ url }) {
  return json({ hello: url.searchParams.get("name") ?? "world", origin: url.origin });
}

export async function POST({ request }) {
  return json({ echoed: await request.json() }, { status: 201 });
}
