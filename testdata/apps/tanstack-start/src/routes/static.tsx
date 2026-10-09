import { createFileRoute } from "@tanstack/react-router";
import { createServerFn } from "@tanstack/react-start";

// Prerendered at build time (vite.config.ts): the stamp is fixed in the
// written HTML, so every request gets the same one.
const stamp = createServerFn({ method: "GET" }).handler(() => ({ at: new Date().toISOString() }));

export const Route = createFileRoute("/static")({
  loader: () => stamp(),
  component: Static,
});

function Static() {
  const { at } = Route.useLoaderData();
  return (
    <main>
      <h1 id="static">Prerendered</h1>
      <p id="stamp">{at}</p>
    </main>
  );
}
