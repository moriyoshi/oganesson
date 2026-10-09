import { useState } from "react";
import type { Route } from "./+types/home";

export function meta({}: Route.MetaArgs) {
  return [{ title: "og react-router" }];
}

export async function loader({ request }: Route.LoaderArgs) {
  const q = new URL(request.url).searchParams.get("q") ?? "nobody";
  return { greeting: `hello ${q}`, runtime: typeof process === "object" ? "server" : "?" };
}

export default function Home({ loaderData }: Route.ComponentProps) {
  const [n, setN] = useState(0);
  return (
    <main className="p-4">
      <h1 className="brand text-2xl">{loaderData.greeting}</h1>
      <p id="runtime">{loaderData.runtime}</p>
      <button id="count" onClick={() => setN(n + 1)}>
        count {n}
      </button>
    </main>
  );
}
