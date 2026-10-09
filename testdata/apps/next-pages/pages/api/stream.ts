import type { NextApiRequest, NextApiResponse } from "next";

export default async function handler(_req: NextApiRequest, res: NextApiResponse) {
  res.writeHead(200, { "content-type": "text/plain; charset=utf-8" });
  res.write("one\n");
  await new Promise((r) => setTimeout(r, 10));
  res.end("two\n");
}
