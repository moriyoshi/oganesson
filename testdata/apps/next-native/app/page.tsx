import { sha256 } from "og:hash";

// Static: prerendered by og build, with the Go functions.
export default function Home() {
  const d = sha256("oganesson");
  return (
    <main>
      <h1>og native</h1>
      <p id="digest">{d.algorithm}:{d.hex}</p>
    </main>
  );
}
