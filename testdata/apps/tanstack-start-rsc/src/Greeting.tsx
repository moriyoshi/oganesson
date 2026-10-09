import "./greeting.css";
import { Counter } from "./Counter";
import { readVisits } from "./visitStore";

// A server component: async, rendered only in the rsc environment.
export async function Greeting({ name }: { name: string }) {
  const at = await Promise.resolve("rsc");
  const visits = await readVisits();
  return (
    <section id="greeting" className="greeting">
      <h1 id="hello">Hello, {name}, from a server component</h1>
      <p id="env">rendered in {at} ({typeof window === "undefined" ? "server" : "browser"})</p>
      <p id="visits">visits {visits}</p>
      <Counter start={5} />
    </section>
  );
}
