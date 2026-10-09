import { render } from "preact";
import { useState } from "preact/hooks";
import "./style.css";

function App() {
  const [n, setN] = useState(1);
  return (
    <main>
      <h1 class="title">Hello from Preact</h1>
      <button id="inc" onClick={() => setN(n + 1)}>count {n}</button>
    </main>
  );
}

render(<App />, document.getElementById("app")!);
