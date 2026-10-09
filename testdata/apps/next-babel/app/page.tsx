import { ClientMark } from "./client";
import { buildInfo } from "../lib/build-info.generated.js";
// Extensionless: Next's resolve.extensions include .mjs.
import { resolvedFrom } from "../lib/extensionless";

declare const __BABEL_MARK__: string;

type Props = { title?: string };

function Title({ title = "og babel" }: Props) {
  return <h1>{title}</h1>;
}

export default function Home() {
  return (
    <main>
      <Title />
      <p id="mark">{__BABEL_MARK__}</p>
      <ClientMark />
      <p id="build-info">{buildInfo.label} {buildInfo.mode}</p>
      <p id="mjs">{resolvedFrom}</p>
    </main>
  );
}
