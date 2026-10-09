import type { NextApiRequest, NextApiResponse } from "next";

export default function handler(req: NextApiRequest, res: NextApiResponse) {
  if (req.query.id === "boom") {
    throw new Error("boom");
  }
  if (req.query.id === "me") {
    res.redirect(307, "/api/users/1");
    return;
  }
  res.json({ id: req.query.id, extra: req.query.extra ?? null, cookie: req.cookies.session ?? null });
}
