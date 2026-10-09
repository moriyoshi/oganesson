import type { NextApiRequest, NextApiResponse } from "next";

// On-demand revalidation: /api/revalidate?path=/about[&only=1]
export default async function handler(req: NextApiRequest, res: NextApiResponse) {
  try {
    await res.revalidate(String(req.query.path), { unstable_onlyGenerated: req.query.only === "1" });
    res.json({ revalidated: true });
  } catch (e) {
    res.status(500).json({ error: String((e as Error).message) });
  }
}
