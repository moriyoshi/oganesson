import { createFileRoute } from "@tanstack/react-router";

// Selective SSR: ssr: false renders neither loader nor component on the
// server; the browser does both.
export const Route = createFileRoute("/client-only")({
  ssr: false,
  loader: () => ({ at: typeof window === "undefined" ? "server" : "browser" }),
  component: ClientOnly,
});

function ClientOnly() {
  const { at } = Route.useLoaderData();
  return <p id="client-only">client-only page, loaded in {at}</p>;
}
