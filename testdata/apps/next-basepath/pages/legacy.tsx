export function getStaticProps() {
  return { props: { from: "getStaticProps" } };
}

export default function Legacy({ from }: { from: string }) {
  return <h1>legacy {from}</h1>;
}
