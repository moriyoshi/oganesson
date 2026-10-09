"use client";
import { useState } from "react";

// A client component inside a server component: shipped as a client
// reference in the Flight stream and hydrated in the browser.
export function Counter({ start }: { start: number }) {
  const [n, setN] = useState(start);
  return (
    <button id="counter" onClick={() => setN(n + 1)}>
      count {n}
    </button>
  );
}
