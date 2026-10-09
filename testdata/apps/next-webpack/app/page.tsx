import Logo from "./logo.svg";
import Message from "./message.mdx";
import { greeting } from "@greeting";
import { BuiltFor } from "./client";
import note from "./note.txt";

export default function Home() {
  return (
    <main>
      <Logo id="logo" />
      <Message />
      <p id="greeting">{greeting}</p>
      <p id="txt">{note}</p>
      <p id="server-built-for">server built for {process.env.BUILT_FOR}</p>
      <BuiltFor />
    </main>
  );
}
