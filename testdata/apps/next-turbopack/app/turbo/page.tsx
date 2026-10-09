// next.config's turbopack section (Next 16 builds with Turbopack): a rule
// with JavaScript output, one with CSS output, and a resolveAlias.
// @ts-ignore
import banner from "./banner.upper.data";
import "./theme.tcss";
// @ts-ignore
import { turboGreeting } from "@turbo-greeting";

export default function Turbo() {
  return (
    <main>
      <p id="turbo-banner">{banner}</p>
      <p id="turbo-alias">{turboGreeting}</p>
      <p id="bundler">{process.env.OG_FIXTURE_BUNDLER ?? "turbopack"}</p>
    </main>
  );
}
