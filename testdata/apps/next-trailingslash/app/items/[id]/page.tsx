export function generateStaticParams() {
  return [{ id: "1" }];
}

export default async function Item({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  return <main><h1 id="item">item {id}</h1></main>;
}
