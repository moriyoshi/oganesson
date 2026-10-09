import type { GetStaticProps } from "next";

// ctx renders locale/defaultLocale so a next start oracle (and the test) can
// observe the defaultLocale getStaticProps receives under i18n.domains.
export const getStaticProps: GetStaticProps<{ ctx: string }> = async ({ locale, defaultLocale }) => ({
  props: { ctx: `${locale}/${defaultLocale}` },
});

export default function Home({ ctx }: { ctx: string }) {
  return (
    <main>
      <h1 id="greeting">Hello</h1>
      <p id="ctx">{ctx}</p>
    </main>
  );
}
