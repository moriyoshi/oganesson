import type { NextConfig } from "next";

// Cache Components: "use cache", cacheLife / cacheTag, partial prerendering,
// a custom cacheLife profile, and a custom handler for "use cache: kv".
const nextConfig: NextConfig = {
  cacheComponents: true,
  cacheLife: { brief: { stale: 1, revalidate: 1, expire: 60 } },
  cacheHandlers: { kv: require.resolve("./lib/kv-cache-handler.ts") },
};

export default nextConfig;
