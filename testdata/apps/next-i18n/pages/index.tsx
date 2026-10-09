import type { GetStaticProps } from "next";

const hello: Record<string, string> = { en: "Hello", fr: "Bonjour", "nl-NL": "Hallo" };

export const getStaticProps: GetStaticProps<{ greeting: string; locale: string }> = async ({ locale, locales, defaultLocale }) => ({
  props: { greeting: hello[locale ?? "en"], locale: `${locale}/${defaultLocale}/${locales?.length}` },
});

export default function Home({ greeting, locale }: { greeting: string; locale: string }) {
  return (
    <main>
      <h1 id="greeting">{greeting}</h1>
      <p id="ctx">{locale}</p>
    </main>
  );
}
