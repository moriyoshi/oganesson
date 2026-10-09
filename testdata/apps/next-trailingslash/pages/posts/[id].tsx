export const getStaticPaths = async () => ({
  paths: [{ params: { id: "1" } }],
  fallback: false,
});

export const getStaticProps = async ({ params }: { params: { id: string } }) => ({
  props: { id: params.id },
});

export default function Post({ id }: { id: string }) {
  return (
    <main>
      <h1 id="post">post {id}</h1>
    </main>
  );
}
