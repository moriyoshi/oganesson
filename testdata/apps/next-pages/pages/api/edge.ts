export const config = { runtime: "edge" };

export default function handler(req: Request) {
  return new Response(JSON.stringify({ edge: true, method: req.method }), {
    headers: { "content-type": "application/json" },
  });
}
