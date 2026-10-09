import type { GetStaticPaths, GetStaticProps } from "next";

export const getStaticPaths: GetStaticPaths = async ({ locales }) => ({
  paths: (locales ?? []).flatMap((locale) => [{ params: { id: "1" }, locale }]),
  fallback: false,
});

export const getStaticProps: GetStaticProps<{ id: string; locale: string }> = async ({ params, locale }) => ({
  props: { id: String(params?.id), locale: String(locale) },
});

export default function Post({ id, locale }: { id: string; locale: string }) {
  return <h1 id="post">post {id} ({locale})</h1>;
}
