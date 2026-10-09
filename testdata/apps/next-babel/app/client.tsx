"use client";

import { useState } from "react";

declare const __BABEL_MARK__: string;

export function ClientMark(): React.ReactElement {
  const [mark] = useState<string>(__BABEL_MARK__);
  return <p id="client-mark">{mark}</p>;
}
