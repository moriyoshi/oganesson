import { defineConfig } from "vitepress";

export default defineConfig({
  title: "VitePress on Oganesson",
  description: "A VitePress build fixture",
  cleanUrls: true,
  lastUpdated: false,
  markdown: { linkify: false },
  vite: { build: { cssMinify: false } },
  themeConfig: {
    search: { provider: "local" },
    nav: [{ text: "Guide", link: "/guide" }],
  },
});
