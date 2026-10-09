import { Suspense } from "react";
import { cookies } from "next/headers";

// "use cache: private" may read the request's cookies; its result is
// cached per request (Next's resume data cache), never in a shared handler.
async function greeting() {
  "use cache: private";
  const name = (await cookies()).get("name")?.value ?? "anonymous";
  return `hello ${name}`;
}

async function Greeting() {
  return <p id="greeting">{await greeting()}</p>;
}

export default function Private() {
  return (
    <Suspense fallback={<p id="greeting">loading</p>}>
      <Greeting />
    </Suspense>
  );
}
