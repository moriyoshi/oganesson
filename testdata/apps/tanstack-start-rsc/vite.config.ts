import { defineConfig } from "vite";
import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import rsc from "@vitejs/plugin-rsc";
import viteReact from "@vitejs/plugin-react";

// TanStack Start's React Server Components variant: a third Vite
// environment ("rsc", built with the react-server condition) renders
// server components into a Flight stream that SSR and the browser decode.
export default defineConfig({
  plugins: [tanstackStart({ rsc: { enabled: true } }), rsc(), viteReact()],
});
