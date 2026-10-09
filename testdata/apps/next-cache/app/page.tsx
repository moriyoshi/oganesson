import { Suspense } from "react";
import { cookies } from "next/headers";
import { stamp } from "../lib/data";

// Dynamic: reads the request's cookies, inside a Suspense boundary (a hole
// in the static shell).
async function Visitor() {
  const name = (await cookies()).get("name")?.value ?? "anonymous";
  return <p id="visitor">visitor {name}</p>;
}

export default async function Home() {
  return (
    <main>
      <p id="stamp">{await stamp("home")}</p>
      <Suspense fallback={<p id="visitor">loading</p>}>
        <Visitor />
      </Suspense>
    </main>
  );
}
