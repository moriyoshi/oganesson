import type { NextConfig } from "next";

// experimental.globalNotFound enables the app/global-not-found full-document
// 404 convention (Next 16.3 App Router).
const nextConfig: NextConfig = {
  experimental: { globalNotFound: true },
};

export default nextConfig;
