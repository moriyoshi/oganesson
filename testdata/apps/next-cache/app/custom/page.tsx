import { Suspense } from "react";
import { connection } from "next/server";
import { kvStamp } from "../../lib/data";

// A "use cache: kv" function, called at request time (after connection()),
// so the custom handler serves it from og:kv on every request.
async function Stamp() {
  await connection();
  return <p id="kv-stamp">{await kvStamp()}</p>;
}

export default function Custom() {
  return (
    <Suspense fallback={<p id="kv-stamp">loading</p>}>
      <Stamp />
    </Suspense>
  );
}
