import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // TypeORM loads its drivers with a computed require(): Node must load it
  // from node_modules, as Next's docs advise for such packages.
  serverExternalPackages: ["typeorm"],
  // Drizzle's migrator reads ./drizzle at run time.
  outputFileTracingIncludes: { "/api/migrate": ["./drizzle/**/*"] },
};

export default nextConfig;
