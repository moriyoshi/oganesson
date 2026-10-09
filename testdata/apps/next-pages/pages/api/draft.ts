import type { NextApiRequest, NextApiResponse } from "next";

// /api/draft?on=1 enables draft mode, /api/draft disables it;
// /api/draft?preview=1 sets preview data (the legacy Preview Mode API).
export default function handler(req: NextApiRequest, res: NextApiResponse) {
  if (req.query.preview === "1") {
    res.setPreviewData({ who: "editor" });
  } else {
    res.setDraftMode({ enable: req.query.on === "1" });
  }
  res.json({ draftMode: req.draftMode ?? null, preview: req.preview ?? null });
}
