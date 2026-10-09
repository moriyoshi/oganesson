export default defineEventHandler(async (event) => {
  if (event.method === "POST") {
    return { echoed: await readBody(event) };
  }
  const { name } = getQuery(event);
  return { greeting: `hello ${name ?? "world"}`, runtime: typeof process === "object" ? "server" : "?" };
});
