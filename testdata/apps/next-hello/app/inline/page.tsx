import { cookies } from "next/headers";

// A module-level inline action: no closure.
async function reset() {
  "use server";
  (await cookies()).delete("greeted");
}

export default async function InlineActions({ searchParams }: { searchParams: Promise<{ who?: string }> }) {
  const who = (await searchParams).who ?? "nobody";
  // The action reads user.name and user.profile.greeting only; user also
  // holds a function, which a bound argument could not carry.
  const user = { name: who, profile: { greeting: "hi" }, format: (s: string) => s.trim() };
  async function greet(form: FormData) {
    "use server";
    const note = String(form.get("note"));
    // A nested inline action, closing over greet's own local (note), not the
    // component's user that greet closes over: hoisted too, its bound value
    // encrypted where it is written and decrypted when it runs.
    async function noteSuffix() {
      "use server";
      return note;
    }
    const jar = await cookies();
    jar.set("greeted", `${user.profile.greeting} ${user.name} ${await noteSuffix()}`);
  }
  const greeted = (await cookies()).get("greeted")?.value ?? "none";
  return (
    <main>
      <p id="greeted">{greeted}</p>
      <form action={greet}>
        <input name="note" defaultValue="n1" />
        <button id="greet" type="submit">greet</button>
      </form>
      <form action={reset}>
        <button id="clear" type="submit">clear</button>
      </form>
    </main>
  );
}
