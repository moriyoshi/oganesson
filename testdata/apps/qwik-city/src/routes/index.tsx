import { component$, useSignal, useStylesScoped$ } from "@builder.io/qwik";
import { Link, routeLoader$, type DocumentHead } from "@builder.io/qwik-city";
// An image import: vite-imagetools (with sharp) makes the variants at build time.
import Logo from "../media/logo.png?jsx";

// Runs on the server for every request (and as JSON for client navigation).
export const useGreeting = routeLoader$(({ url }) => {
  return { text: `hello ${url.searchParams.get("q") ?? "nobody"}`, runtime: typeof process === "object" ? "server" : "?" };
});

export default component$(() => {
  useStylesScoped$(`.brand { color: #016306; }`);
  const greeting = useGreeting();
  const count = useSignal(0);
  return (
    <>
      <h1 class="brand">{greeting.value.text}</h1>
      <p id="runtime">{greeting.value.runtime}</p>
      <button id="inc" onClick$={() => count.value++}>
        count {count.value}
      </button>
      <Logo id="logo" alt="logo" />
      <Link href="/about/" id="to-about">about</Link>
    </>
  );
});

export const head: DocumentHead = {
  title: "og qwik",
};
