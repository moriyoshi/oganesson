"use strict";
'use client';
// A client component whose "use client" follows another directive, as in
// TypeScript's CommonJS output (@next/third-parties).
import { useState } from "react";

export default function LegacyClient() {
  const [n] = useState(5);
  return <span id="legacy-client">legacy client {n}</span>;
}
