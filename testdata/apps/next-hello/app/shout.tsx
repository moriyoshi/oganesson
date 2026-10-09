"use client";
import { useState } from "react";
import { shout } from "./actions";

export default function Shout() {
  const [result, setResult] = useState("");
  return (
    <p>
      <button id="shout" onClick={async () => setResult((await shout("hello")).text)}>shout</button>{" "}
      <span id="shout-result">{result}</span>
    </p>
  );
}
