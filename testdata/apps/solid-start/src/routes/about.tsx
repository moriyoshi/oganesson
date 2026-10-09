import { Title } from "@solidjs/meta";
import { action, useSubmission } from "@solidjs/router";

const shout = action(async (form: FormData) => {
  "use server";
  return String(form.get("text") ?? "").toUpperCase();
}, "shout");

export default function About() {
  const submission = useSubmission(shout);
  return (
    <main>
      <Title>About</Title>
      <h1>About</h1>
      <form action={shout} method="post">
        <input name="text" value="quiet" />
        <button id="shout" type="submit">shout</button>
      </form>
      <p id="result">{submission.result}</p>
    </main>
  );
}
