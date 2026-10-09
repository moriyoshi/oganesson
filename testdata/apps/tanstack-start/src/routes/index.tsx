import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { createServerFn } from "@tanstack/react-start";

// Runs on the server only; the client calls it over RPC.
const getGreeting = createServerFn({ method: "GET" }).handler(async () => {
  return { message: "Hello from the server", runtime: typeof window === "undefined" ? "server" : "client" };
});

const echo = createServerFn({ method: "POST" })
  .inputValidator((d: { text: string }) => d)
  .handler(async ({ data }) => ({ echoed: data.text.toUpperCase() }));

export const Route = createFileRoute("/")({
  loader: () => getGreeting(),
  component: Home,
});

function Home() {
  const data = Route.useLoaderData();
  const [echoed, setEchoed] = useState("");
  return (
    <main>
      <h1 id="message">{data.message}</h1>
      <p id="runtime">{data.runtime}</p>
      <button id="echo" onClick={async () => setEchoed((await echo({ data: { text: "ping" } })).echoed)}>
        echo
      </button>
      <p id="echoed">{echoed}</p>
    </main>
  );
}
