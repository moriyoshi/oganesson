import { createFileRoute } from "@tanstack/react-router";

export const Route = createFileRoute("/posts/$postId")({
  loader: ({ params }) => ({ title: `Post ${params.postId}` }),
  component: Post,
});

function Post() {
  const { title } = Route.useLoaderData();
  return <h1 id="post">{title}</h1>;
}
