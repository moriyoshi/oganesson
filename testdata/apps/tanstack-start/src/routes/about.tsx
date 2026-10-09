import { createFileRoute } from "@tanstack/react-router";
import { createMiddleware, createServerFn } from "@tanstack/react-start";

// Function middleware: adds context the server function reads.
const withUser = createMiddleware({ type: "function" }).server(({ next }) => next({ context: { user: "og-user" } }));

const whoami = createServerFn({ method: "GET" })
  .middleware([withUser])
  .handler(({ context }) => ({ user: context.user }));

export const Route = createFileRoute("/about")({
  loader: () => whoami(),
  component: About,
});

function About() {
  const { user } = Route.useLoaderData();
  return (
    <main>
      <h1 id="about">About TanStack Start</h1>
      <p id="user">{user}</p>
    </main>
  );
}
