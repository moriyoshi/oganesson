import { useState } from "react";
import Head from "next/head";
import styles from "../styles/Home.module.css";

export default function Home() {
  const [n, setN] = useState(0);
  return (
    <>
      <Head>
        <title>next-pages home</title>
      </Head>
      <h1 className={styles.title}>Hello, Pages Router</h1>
      <button id="counter" onClick={() => setN(n + 1)}>
        clicked {n} times
      </button>
    </>
  );
}
