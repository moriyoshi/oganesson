import { redirect } from "next/navigation";

// A server action that redirects: called from the client router, Next
// fetches the target's RSC payload from the server's own origin
// (__NEXT_PRIVATE_ORIGIN) and returns it with the action's response.
async function go() {
  "use server";
  redirect("/static");
}

export default function Go() {
  return (
    <form action={go}>
      <button>go</button>
    </form>
  );
}
