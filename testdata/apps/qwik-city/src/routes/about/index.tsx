import { component$ } from "@builder.io/qwik";
import { Form, Link, routeAction$ } from "@builder.io/qwik-city";

// A server action: works as a plain form post, and from the client.
export const useShout = routeAction$((data) => {
  return { shouted: String(data.text ?? "").toUpperCase() };
});

export default component$(() => {
  const shout = useShout();
  return (
    <>
      <h1>About</h1>
      <Form action={shout}>
        <input name="text" value="quiet" />
        <button id="shout" type="submit">shout</button>
      </Form>
      <p id="result">{shout.value?.shouted}</p>
      <Link href="/?q=back" id="to-home">home</Link>
    </>
  );
});
