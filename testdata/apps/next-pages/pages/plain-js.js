// JSX in a .js file, as Next's SWC accepts in a project's own code.
import { useState } from "react";

export async function getServerSideProps() {
  return { props: { from: "js" } };
}

export default function PlainJS({ from }) {
  const [n] = useState(3);
  return <p id="plain-js">jsx in {from} {n}</p>;
}
