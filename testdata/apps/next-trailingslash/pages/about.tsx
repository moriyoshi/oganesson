export const getStaticProps = async () => ({ props: { where: "about" } });

export default function About({ where }: { where: string }) {
  return <main><h1 id="where">{where}</h1></main>;
}
