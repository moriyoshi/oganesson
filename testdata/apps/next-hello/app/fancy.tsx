"use client";
import { useState } from "react";

export default function Fancy() {
  const [on, setOn] = useState(false);
  return (
    <p id="fancy" className={on ? "on" : ""} onClick={() => setOn(!on)}>
      styled-jsx
      <style jsx>{`
        p {
          color: teal;
          cursor: pointer;
        }
        .on {
          font-weight: bold;
        }
      `}</style>
    </p>
  );
}
