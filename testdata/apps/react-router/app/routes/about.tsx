export function loader() {
  return { builtAt: "prerendered" };
}

export default function About({ loaderData }: { loaderData: { builtAt: string } }) {
  return (
    <main className="p-4">
      <h1>About</h1>
      <p id="built">{loaderData.builtAt}</p>
    </main>
  );
}
