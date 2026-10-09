import { type RouteConfig, index, route } from "@react-router/dev/routes";

export default [
  index("routes/home.tsx"),
  route("about", "routes/about.tsx"),
  route("todos", "routes/todos.tsx"),
  route("api/hello", "routes/api.hello.ts"),
] satisfies RouteConfig;
