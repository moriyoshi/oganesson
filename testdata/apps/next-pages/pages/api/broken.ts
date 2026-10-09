// A route whose module throws when it loads (as with-sentry's test route
// does): it fails on its own, the rest of the app serves.
function work() {
  throw new Error("broken at load");
}

work();

export default function handler(_req: unknown, res: { status(n: number): { json(v: unknown): void } }) {
  res.status(200).json({ ok: true });
}
