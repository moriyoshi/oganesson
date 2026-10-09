"use client";
import { useState } from "react";
import { usePathname } from "next/navigation";
import styles from "./counter.module.css";

export default function Counter() {
  const [n, setN] = useState(0);
  const pathname = usePathname();
  return (
    <p>
      <button id="counter" className={styles.button} onClick={() => setN(n + 1)}>
        clicked {n} times
      </button>{" "}
      <span id="pathname">on {pathname}</span>
    </p>
  );
}
