import type { GetStaticPaths, GetStaticProps } from "next";

const posts: Record<string, string> = { "1": "First", "2": "Second" };

export const getStaticPaths: GetStaticPaths = async () => ({
  paths: [{ params: { id: "1" } }, { params: { id: "2" } }],
  fallback: false,
});

export const getStaticProps: GetStaticProps<{ id: string; title: string }> = async ({ params }) => ({
  props: { id: String(params?.id), title: posts[String(params?.id)] },
});

export default function PostPage({ id, title }: { id: string; title: string }) {
  return <h1 id="post">{id}: {title}</h1>;
}
