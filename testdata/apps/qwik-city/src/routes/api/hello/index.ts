import type { RequestHandler } from "@builder.io/qwik-city";

export const onGet: RequestHandler = async ({ json, query }) => {
  json(200, { hello: query.get("name") ?? "world" });
};
