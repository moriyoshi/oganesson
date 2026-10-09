import { createFileRoute } from "@tanstack/react-router";
import { createServerFn } from "@tanstack/react-start";
import { renderServerComponent } from "@tanstack/react-start/rsc";
import { Greeting } from "../Greeting";
import { BumpButton } from "../BumpButton";

const getGreeting = createServerFn({ method: "GET" }).handler(async () => renderServerComponent(<Greeting name="og" />));

export const Route = createFileRoute("/")({
  loader: async () => ({ greeting: await getGreeting() }),
  component: Home,
});

function Home() {
  const { greeting } = Route.useLoaderData();
  // The mutation's button sits in the route component: Start registers
  // server functions it finds compiling the route graph, which does not
  // enter client components inside server components (as upstream).
  return (
    <main>
      {greeting}
      <BumpButton />
    </main>
  );
}
