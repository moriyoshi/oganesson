import type { GetStaticPaths, GetStaticProps } from "next";
import { useRouter } from "next/router";

// fallback: true: other ids first get the fallback shell (router.isFallback),
// then the client fetches the data and renders the page.
export const getStaticPaths: GetStaticPaths = async () => ({
  paths: [{ params: { id: "1" } }],
  fallback: true,
});

export const getStaticProps: GetStaticProps<{ id: string }> = async ({ params }) => ({
  props: { id: String(params?.id) },
});

export default function Item({ id }: { id: string }) {
  const router = useRouter();
  if (router.isFallback) return <p id="loading">Loading item…</p>;
  return <h1 id="item">item {id}</h1>;
}
