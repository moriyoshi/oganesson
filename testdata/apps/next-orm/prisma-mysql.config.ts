import { defineConfig } from "prisma/config";

// A separate config for the MySQL client so `prisma generate --config
// prisma-mysql.config.ts` does not inherit the default prisma.config.ts's sqlite
// `datasource.url` (Prisma 7 rejects a url reaching the schema datasource when
// an adapter supplies the connection: P1012). generate needs only the schema's
// provider; the adapter-mariadb connection is given to PrismaClient at runtime.
export default defineConfig({
  schema: "prisma/schema-mysql.prisma",
});
