import { Suspense } from "react";
import { connection } from "next/server";
import { briefStamp } from "../../lib/data";

// A function cached with next.config's "brief" profile, called per request.
async function Stamp() {
  await connection();
  return <p id="brief-stamp">{await briefStamp()}</p>;
}

export default function Brief() {
  return (
    <Suspense fallback={<p id="brief-stamp">loading</p>}>
      <Stamp />
    </Suspense>
  );
}
