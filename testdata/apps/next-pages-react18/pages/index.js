// A Pages Router app on React 18, which Next 16 supports there: pages
// render with the project's react and react-dom, not Next's vendored 19.
import { useState, version } from "react";

export async function getServerSideProps() {
  return { props: { greeting: "hello" } };
}

export default function Home({ greeting }) {
  const [count, setCount] = useState(0);
  return (
    <main>
      <p id="greeting">{greeting}</p>
      <p id="react-version">react {version}</p>
      <button id="inc" onClick={() => setCount(count + 1)}>
        clicked {count}
      </button>
    </main>
  );
}
