import { createFileRoute } from "@tanstack/react-router";

export const Route = createFileRoute("/other")({
  component: () => <main><h1 id="other">Other</h1></main>,
});
