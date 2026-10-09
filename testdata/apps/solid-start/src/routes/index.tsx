import { Title } from "@solidjs/meta";
import { createAsync, query, useSearchParams } from "@solidjs/router";
import Counter from "~/components/Counter";

// A query run on the server (a "use server" function), during SSR and as
// RPC on client navigation.
const greeting = query(async (name: string) => {
  "use server";
  return { text: `hello ${name}`, runtime: typeof process === "object" ? "server" : "?" };
}, "greeting");

export default function Home() {
  const [params] = useSearchParams();
  const data = createAsync(() => greeting(String(params.q ?? "nobody")));
  return (
    <main>
      <Title>og solid-start</Title>
      <h1 class="brand">{data()?.text}</h1>
      <p id="runtime">{data()?.runtime}</p>
      <Counter />
    </main>
  );
}
