import { defineConfig, env } from "prisma/config";

// The connection URL is given as an environment reference, not a literal: the
// @prisma/adapter-pg driver supplies the client connection at runtime, and og's
// app-startup `migrate deploy` resolves env("DATABASE_URL") at startup too
// (captured as meta.PrismaMigrate.urlEnv at build).
export default defineConfig({
  schema: "prisma/schema.prisma",
  datasource: { url: env("DATABASE_URL") },
});
