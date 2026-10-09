"use client";

// A client module that throws when it loads on the server (as
// @react-three/fiber 8 does under React 19): only the pages that render it
// fail; next start loads client modules as pages reference them.
if (typeof window === "undefined") {
  throw new Error("og fixture: this client module throws when it loads on the server");
}

export default function Widget() {
  return <p>widget</p>;
}
