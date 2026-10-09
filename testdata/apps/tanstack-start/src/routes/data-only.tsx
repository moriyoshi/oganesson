import { createFileRoute } from "@tanstack/react-router";

// Selective SSR: "data-only" runs the loader on the server and renders the
// component in the browser only.
export const Route = createFileRoute("/data-only")({
  ssr: "data-only",
  loader: () => ({ at: typeof window === "undefined" ? "server" : "browser" }),
  component: DataOnly,
});

function DataOnly() {
  const { at } = Route.useLoaderData();
  return <p id="data-only">data-only page, loaded in {at}</p>;
}
