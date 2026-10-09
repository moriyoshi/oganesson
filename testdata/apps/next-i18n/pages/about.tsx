import type { GetServerSideProps } from "next";

export const getServerSideProps: GetServerSideProps<{ locale: string }> = async ({ locale }) => ({
  props: { locale: String(locale) },
});

export default function About({ locale }: { locale: string }) {
  return <h1 id="about">about in {locale}</h1>;
}
