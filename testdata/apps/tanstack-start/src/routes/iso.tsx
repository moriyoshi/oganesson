import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { createIsomorphicFn } from "@tanstack/react-start";

// One function, an implementation per side: the server build keeps only
// .server(), the client build only .client().
const where = createIsomorphicFn()
  .server(() => "server")
  .client(() => "client");

export const Route = createFileRoute("/iso")({
  loader: () => ({ at: where() }),
  component: Iso,
});

function Iso() {
  const { at } = Route.useLoaderData();
  const [clicked, setClicked] = useState("");
  return (
    <main>
      <p id="iso-loader">{at}</p>
      <button id="iso-run" onClick={() => setClicked(where())}>
        run
      </button>
      <p id="iso-clicked">{clicked}</p>
    </main>
  );
}
