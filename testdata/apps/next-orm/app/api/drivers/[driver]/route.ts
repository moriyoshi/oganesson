// The same scenario on each server driver: DDL, an insert through Drizzle,
// reads through Drizzle and Kysely, a transaction that commits and one
// that fails and rolls back. MYSQL_URL serves mysql2; PG_URL serves pg and
// postgres (postgres.js), unless PGJS_URL serves the latter.
import { sql as dsql, eq } from "drizzle-orm";
import { mysqlTable, int, varchar, boolean as mysqlBool } from "drizzle-orm/mysql-core";
import { pgTable, serial, text, boolean as pgBool } from "drizzle-orm/pg-core";
import { sqliteTable, integer as sqliteInt, text as sqliteText } from "drizzle-orm/sqlite-core";
import { Kysely, MysqlDialect, PostgresDialect } from "kysely";

export const dynamic = "force-dynamic";

const myItems = mysqlTable("og_items", { id: int("id").primaryKey().autoincrement(), name: varchar("name", { length: 64 }).notNull(), done: mysqlBool("done").notNull().default(false) });
const pgItems = pgTable("og_items", { id: serial("id").primaryKey(), name: text("name").notNull(), done: pgBool("done").notNull().default(false) });
const sqliteItems = sqliteTable("og_items", { id: sqliteInt("id").primaryKey({ autoIncrement: true }), name: sqliteText("name").notNull(), done: sqliteInt("done", { mode: "boolean" }).notNull().default(false) });

async function scenario(db: any, items: any, run: (q: string) => Promise<unknown>, kysely: Kysely<any> | null, ownsPool = true) {
  const out: Record<string, unknown> = {};
  await run("DROP TABLE IF EXISTS og_items");
  await run(items === myItems
    ? "CREATE TABLE og_items (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(64) NOT NULL, done BOOLEAN NOT NULL DEFAULT FALSE)"
    : items === sqliteItems
    ? "CREATE TABLE og_items (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, done INTEGER NOT NULL DEFAULT 0)"
    : "CREATE TABLE og_items (id SERIAL PRIMARY KEY, name TEXT NOT NULL, done BOOLEAN NOT NULL DEFAULT FALSE)");
  await db.insert(items).values([{ name: "alpha" }, { name: "beta", done: true }]);
  out.drizzle = await db.select({ id: items.id, name: items.name, done: items.done }).from(items).orderBy(items.id);
  await db.transaction(async (tx: any) => {
    await tx.update(items).set({ done: true }).where(eq(items.name, "alpha"));
  });
  try {
    await db.transaction(async (tx: any) => {
      await tx.delete(items);
      await tx.execute(dsql`SELECT * FROM og_missing`);
    });
    out.rollback = "no error";
  } catch {
    out.rollback = "failed";
  }
  out.count = (await db.select({ n: dsql<number>`count(*)` }).from(items))[0].n;
  if (kysely) {
    out.kysely = await kysely.selectFrom("og_items").select(["name", "done"]).where("done", "=", true).orderBy("id").execute();
    if (ownsPool) await kysely.destroy();
  }
  return out;
}

export async function GET(_req: Request, { params }: { params: Promise<{ driver: string }> }) {
  const { driver } = await params;
  try {
    switch (driver) {
      case "mysql2": {
        const mysql = (await import("mysql2/promise")).default;
        const { drizzle } = await import("drizzle-orm/mysql2");
        const pool = mysql.createPool({ uri: process.env.MYSQL_URL!, connectionLimit: 2 });
        const kysely = new Kysely<any>({ dialect: new MysqlDialect({ pool: (await import("mysql2")).createPool({ uri: process.env.MYSQL_URL!, connectionLimit: 1 }) }) });
        try {
          return Response.json(await scenario(drizzle(pool), myItems, (q) => pool.query(q), kysely));
        } finally {
          await pool.end();
        }
      }
      case "pg": {
        const pg = (await import("pg")).default;
        const { drizzle } = await import("drizzle-orm/node-postgres");
        // One pool for both (one connection): PGlite serves one session at
        // a time.
        const pool = new pg.Pool({ connectionString: process.env.PG_URL, max: 1 });
        const kysely = new Kysely<any>({ dialect: new PostgresDialect({ pool }) });
        try {
          return Response.json(await scenario(drizzle(pool), pgItems, (q) => pool.query(q), kysely, false));
        } finally {
          await pool.end();
        }
      }
      case "pg-native": {
        // pg-native is not installed: pg's lazy getter catches the
        // require's MODULE_NOT_FOUND and answers null.
        const pg = (await import("pg")).default;
        return Response.json({ native: pg.native });
      }
      case "postgres": {
        const postgres = (await import("postgres")).default;
        const { drizzle } = await import("drizzle-orm/postgres-js");
        const client = postgres(process.env.PGJS_URL ?? process.env.PG_URL!, { max: 1, onnotice: () => {} });
        try {
          return Response.json(await scenario(drizzle(client), pgItems, (q) => client.unsafe(q), null));
        } finally {
          await client.end();
        }
      }
      case "libsql": {
        const { createClient } = await import("@libsql/client");
        const { drizzle } = await import("drizzle-orm/libsql");
        const client = createClient({ url: process.env.LIBSQL_URL! });
        try {
          return Response.json(await scenario(drizzle(client), sqliteItems, (q) => client.execute(q), null));
        } finally {
          client.close();
        }
      }
    }
    return new Response("unknown driver", { status: 404 });
  } catch (e) {
    const err = e as Error & { code?: string };
    return Response.json({ error: err.message, code: err.code, stack: err.stack?.split("\n").slice(0, 6) }, { status: 500 });
  }
}
