import type { APIRoute } from "astro";

export const GET: APIRoute = ({ url }) => Response.json({ hello: url.searchParams.get("name") ?? "world" });

export const POST: APIRoute = async ({ request }) => Response.json({ received: await request.json() }, { status: 201 });
