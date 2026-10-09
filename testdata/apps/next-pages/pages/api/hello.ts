import type { NextApiRequest, NextApiResponse } from "next";

export default function handler(req: NextApiRequest, res: NextApiResponse) {
  if (req.method === "POST") {
    res.status(201).json({ received: req.body });
    return;
  }
  res.setHeader("x-api", "pages");
  res.status(200).json({ hello: req.query.name ?? "world" });
}
