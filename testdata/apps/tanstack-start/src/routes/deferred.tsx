import { Await, createFileRoute } from "@tanstack/react-router";

// The loader returns before the slow promise settles: the page streams the
// fallback first, then the resolved value.
export const Route = createFileRoute("/deferred")({
  loader: () => ({
    fast: "fast data",
    slow: new Promise<string>((resolve) => setTimeout(() => resolve("slow data"), 1500)),
  }),
  component: Deferred,
});

function Deferred() {
  const { fast, slow } = Route.useLoaderData();
  return (
    <main>
      <p id="fast">{fast}</p>
      <Await promise={slow} fallback={<p id="slow-fallback">loading slow…</p>}>
        {(value) => <p id="slow">{value}</p>}
      </Await>
    </main>
  );
}
