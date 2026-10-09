import type { Route } from "./+types/api.hello";

export function loader({ request }: Route.LoaderArgs) {
  return Response.json({ hello: new URL(request.url).searchParams.get("name") ?? "world" });
}

export async function action({ request }: Route.ActionArgs) {
  const body = await request.json();
  return Response.json({ echoed: body });
}
