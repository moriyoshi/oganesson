import type { ReactNode } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { createServerFn } from "@tanstack/react-start";
import { CompositeComponent, createCompositeComponent } from "@tanstack/react-start/rsc";

// A Composite Component: server-rendered framing with slots the client
// fills in (a render prop with server data, and children).
const getCard = createServerFn({ method: "GET" }).handler(async () =>
  createCompositeComponent((props: { renderActions?: (d: { id: string }) => ReactNode; children?: ReactNode }) => (
    <article id="card">
      <h2 id="card-title">Server card</h2>
      <div id="actions">{props.renderActions?.({ id: "42" })}</div>
      <div id="kids">{props.children}</div>
    </article>
  )),
);

export const Route = createFileRoute("/composite")({
  loader: async () => ({ card: await getCard() }),
  component: Composite,
});

function Composite() {
  const { card } = Route.useLoaderData();
  return (
    <main>
      <CompositeComponent src={card} renderActions={({ id }: { id: string }) => <button id="act">act {id}</button>}>
        <p id="slot-child">client child</p>
      </CompositeComponent>
    </main>
  );
}
