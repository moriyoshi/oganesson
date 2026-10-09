import { render } from "solid-js/web";
import { createSignal } from "solid-js";
import "./style.css";

function App() {
  const [n, setN] = createSignal(1);
  return (
    <main>
      <h1 class="title">Hello from Solid</h1>
      <button id="inc" onClick={() => setN(n() + 1)}>count {n()}</button>
      <p id="double">double {n() * 2}</p>
    </main>
  );
}

render(() => <App />, document.getElementById("app")!);
