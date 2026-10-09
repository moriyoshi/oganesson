import Link from "next/link";
import type { GetServerSideProps } from "next";

export const getServerSideProps: GetServerSideProps<{ slug: string }> = async ({ params }) => {
  if (params?.slug === "missing") return { notFound: true };
  return { props: { slug: String(params?.slug) } };
};

export default function Post({ slug }: { slug: string }) {
  return (
    <main>
      <h1 id="post">Pages post {slug}</h1>
      <Link href="/dashboard" id="to-dashboard">dashboard (app)</Link>
    </main>
  );
}
