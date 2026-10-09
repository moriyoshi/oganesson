import type { GetStaticProps } from "next";

export const getStaticProps: GetStaticProps<{ builtAt: number }> = async () => ({
  props: { builtAt: Date.now() },
  revalidate: 3600,
});

export default function About({ builtAt }: { builtAt: number }) {
  return <p id="about" data-built={builtAt}>A static page with getStaticProps.</p>;
}
