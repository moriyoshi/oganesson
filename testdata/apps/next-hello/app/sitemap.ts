import type { MetadataRoute } from "next";

export default function sitemap(): MetadataRoute.Sitemap {
  return [
    { url: "https://example.com/", changeFrequency: "daily", priority: 1 },
    { url: "https://example.com/about", priority: 0.5 },
  ];
}
