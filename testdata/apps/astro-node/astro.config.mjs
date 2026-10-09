import { defineConfig } from "astro/config";
import node from "@astrojs/node";

// Server output with the Node adapter's standalone server
// (dist/server/entry.mjs), which og runs as a server program.
export default defineConfig({
  output: "server",
  adapter: node({ mode: "standalone" }),
});
