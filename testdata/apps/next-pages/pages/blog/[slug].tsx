import type { GetServerSideProps } from "next";
import { useRouter } from "next/router";

type Props = { slug: string; ua: string; at: number };

export const getServerSideProps: GetServerSideProps<Props> = async ({ params, req }) => {
  const slug = String(params?.slug);
  if (slug === "missing") return { notFound: true };
  if (slug === "old") return { redirect: { destination: "/blog/hello", permanent: false } };
  return { props: { slug, ua: String(req.headers["user-agent"] ?? ""), at: Date.now() } };
};

export default function Post({ slug, ua }: Props) {
  const router = useRouter();
  return (
    <article>
      <h1 id="slug">Post {slug}</h1>
      <p id="ua">{ua}</p>
      <p id="query">{JSON.stringify(router.query)}</p>
    </article>
  );
}
