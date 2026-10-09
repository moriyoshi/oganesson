import { defineConfig } from "vite";
import { VitePWA } from "vite-plugin-pwa";

// generateSW (the default): workbox-build writes sw.js with a precache
// manifest of the build's assets, bundling its runtime with Rollup.
export default defineConfig({
  plugins: [
    VitePWA({
      registerType: "autoUpdate",
      manifest: { name: "PWA on Oganesson", short_name: "og-pwa", theme_color: "#336699", icons: [] },
    }),
  ],
});
