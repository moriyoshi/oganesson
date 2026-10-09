import { createFileRoute } from "@tanstack/react-router";

// A server route: plain request handlers, no component.
export const Route = createFileRoute("/api/hello")({
  server: {
    handlers: {
      GET: ({ request }) => Response.json({ hello: new URL(request.url).searchParams.get("name") ?? "world" }),
      POST: async ({ request }) => Response.json({ received: await request.json() }, { status: 201 }),
    },
  },
});
