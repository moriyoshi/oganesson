import type { GetStaticPaths, GetStaticProps } from "next";

// fallback: "blocking": paths beyond getStaticPaths render on first request
// and are cached; path strings instead of { params }.
export const getStaticPaths: GetStaticPaths = async () => ({
  paths: ["/docs/a"],
  fallback: "blocking",
});

export const getStaticProps: GetStaticProps<{ slug: string; builtAt: string }> = async ({ params }) => {
  if (params?.slug === "missing") return { notFound: true };
  if (params?.slug === "broken") throw new Error("broken doc");
  return { props: { slug: String(params?.slug), builtAt: `${Date.now()}-${Math.random()}` } };
};

export default function Doc({ slug, builtAt }: { slug: string; builtAt: string }) {
  return (
    <>
      <h1 id="doc">doc {slug}</h1>
      <p id="built">{builtAt}</p>
    </>
  );
}
