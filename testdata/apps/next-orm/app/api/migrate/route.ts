// Drizzle's runtime migrator: it reads ./drizzle (meta/_journal.json and
// the SQL files) at run time, a path tracing cannot see, hence
// outputFileTracingIncludes in next.config.ts.
import Database from "better-sqlite3";
import { drizzle } from "drizzle-orm/better-sqlite3";
import { migrate } from "drizzle-orm/better-sqlite3/migrator";
import { sql } from "drizzle-orm";

export const dynamic = "force-dynamic";

export function GET() {
  const sqlite = new Database(process.env.MIGRATE_DB_FILE ?? "migrate.db");
  try {
    const db = drizzle(sqlite);
    migrate(db, { migrationsFolder: "drizzle" });
    migrate(db, { migrationsFolder: "drizzle" }); // applied once
    return Response.json(db.all(sql`SELECT id, name FROM migrated`));
  } finally {
    sqlite.close();
  }
}
