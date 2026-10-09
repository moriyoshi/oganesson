import { createMiddleware, createStart } from "@tanstack/react-start";

// Global request middleware: runs for page and server-function requests.
const tag = createMiddleware({ type: "request" }).server(async ({ next, pathname, handlerType }) => {
  const result = await next();
  result.response.headers.set("x-og-middleware", `${handlerType}:${pathname}`);
  return result;
});

export const startInstance = createStart(() => ({ requestMiddleware: [tag] }));
