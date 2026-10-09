import { Form } from "react-router";
import type { Route } from "./+types/todos";

// Module state lives per request VM in og (fresh VM per request), so the
// action's result is echoed back instead of stored.
export async function action({ request }: Route.ActionArgs) {
  const form = await request.formData();
  const title = String(form.get("title") ?? "");
  if (!title) return { error: "title required" };
  return { added: title.toUpperCase() };
}

export default function Todos({ actionData }: Route.ComponentProps) {
  return (
    <main className="p-4">
      <h1>Todos</h1>
      <Form method="post">
        <input name="title" />
        <button type="submit">Add</button>
      </Form>
      {actionData && "added" in actionData && <p id="added">added {actionData.added}</p>}
      {actionData && "error" in actionData && <p id="error">{actionData.error}</p>}
    </main>
  );
}
