import { defineConfig } from "vite";
import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import viteReact from "@vitejs/plugin-react";

// OG_FIXTURE_SPA=1 builds the app in SPA mode (vite_test.go).
const spa = process.env.OG_FIXTURE_SPA === "1";

export default defineConfig({
  plugins: [
    tanstackStart({
      // Only /static is prerendered (and, in SPA mode, the shell, which is
      // the page at spa.maskPath "/"); the other pages render per request.
      prerender: { enabled: true, crawlLinks: false, filter: (page) => page.path === "/static" || (spa && page.path === "/") },
      spa: { enabled: spa },
      // sitemap.xml lists the prerendered pages.
      sitemap: { enabled: true, host: "https://og.example" },
    }),
    viteReact(),
  ],
});
