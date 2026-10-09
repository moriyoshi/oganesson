import Link from "next/link";

export default function Home() {
  return (
    <main>
      <h1 id="app-home">App Router home</h1>
      <Link href="/about" id="to-about">about (pages)</Link> · <Link href="/dashboard" id="to-dashboard">dashboard (app)</Link>
    </main>
  );
}
