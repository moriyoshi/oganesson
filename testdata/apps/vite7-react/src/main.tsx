import { StrictMode, Suspense, lazy, useState } from "react";
import { createRoot } from "react-dom/client";
import data from "@/data.json";
import "./app.css";

const Lazy = lazy(() => import("./Lazy"));

declare const __BUILD_FLAVOR__: string;

// import.meta.glob (eager): every locale module, keyed by path.
const locales = import.meta.glob<string>("./locales/*.ts", { eager: true, import: "default" });

function App() {
  const [n, setN] = useState(0);
  const [show, setShow] = useState(false);
  return (
    <main>
      <h1 id="title">{data.title}</h1>
      <p id="env">
        {import.meta.env.MODE}/{String(import.meta.env.PROD)}/{import.meta.env.VITE_GREETING}/{__BUILD_FLAVOR__}
      </p>
      <p id="locales">{Object.entries(locales).map(([k, v]) => `${k}=${v}`).join(",")}</p>
      <button id="count" onClick={() => setN(n + 1)}>
        count {n}
      </button>
      <button id="load" onClick={() => setShow(true)}>
        load
      </button>
      {show && (
        <Suspense fallback={<p>loading</p>}>
          <Lazy />
        </Suspense>
      )}
    </main>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
