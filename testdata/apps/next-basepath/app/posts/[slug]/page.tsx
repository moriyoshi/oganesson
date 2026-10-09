export function generateStaticParams() {
  return [{ slug: "first" }];
}

export default async function Post({ params }: { params: Promise<{ slug: string }> }) {
  const { slug } = await params;
  return <h1>post {slug}</h1>;
}
