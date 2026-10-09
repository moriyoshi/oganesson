"use client";

import { useEffect, useState } from "react";
// lib/links-loader.cjs's resolutions, in the SSR and browser builds.
import links from "./names.links";

// A module the browser build gets from next.config's resolve.fallback
// (false: an empty module); the server never loads it.
const nodeOnly = typeof window === "undefined" ? null : require("og-fixture-node-only");

// process.env.BUILT_FOR is defined per target by next.config's
// DefinePlugin; shown once hydrated (the server render has the server's).
const builtFor = "built-for:" + process.env.BUILT_FOR;

export function BuiltFor() {
  const [shown, setShown] = useState("not hydrated");
  useEffect(() => setShown(`${builtFor}, fallback ${typeof nodeOnly}`), []);
  return (
    <>
      <p id="built-for">{shown}</p>
      <p id="links">{JSON.stringify(links)}</p>
    </>
  );
}
