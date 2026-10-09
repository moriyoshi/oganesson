import Link from "next/link";
import type { GetStaticProps } from "next";

export const getStaticProps: GetStaticProps<{ builtAt: number }> = async () => ({ props: { builtAt: Date.now() } });

export default function About({ builtAt }: { builtAt: number }) {
  return (
    <main>
      <h1 id="about" data-built={builtAt}>Pages Router about</h1>
      <Link href="/" id="to-home">home (app)</Link> · <Link href="/blog/hello" id="to-blog">blog (pages)</Link>
    </main>
  );
}
