// ISR for a route handler, with its own Cache-Control (kept as is). An hour,
// so the build's prerender stays fresh for the whole test suite.
export const revalidate = 3600;

export function GET() {
  return new Response(`generated ${Date.now()}`, {
    headers: { "content-type": "text/plain", "cache-control": "public, max-age=30" },
  });
}
