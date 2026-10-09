export default defineNuxtConfig({
  compatibilityDate: "2026-10-01",
  devtools: { enabled: false },
  routeRules: {
    "/about": { prerender: true },
  },
});
