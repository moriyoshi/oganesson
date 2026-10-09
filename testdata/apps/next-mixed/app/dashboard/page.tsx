import Link from "next/link";

export default function Dashboard() {
  return (
    <main>
      <h1 id="dashboard">App Router dashboard</h1>
      <Link href="/blog/hello" id="to-blog">blog (pages)</Link>
    </main>
  );
}
