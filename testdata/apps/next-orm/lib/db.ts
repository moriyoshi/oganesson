// One SQLite file (DB_FILE, else notes.db in the working directory), used
// through Drizzle and through Kysely, both over better-sqlite3.
import Database from "better-sqlite3";
import { drizzle } from "drizzle-orm/better-sqlite3";
import { sql } from "drizzle-orm";
import { integer, sqliteTable, text } from "drizzle-orm/sqlite-core";
import { Kysely, SqliteDialect, type Generated } from "kysely";

export const notes = sqliteTable("notes", {
  id: integer("id").primaryKey({ autoIncrement: true }),
  title: text("title").notNull(),
  body: text("body"),
  done: integer("done", { mode: "boolean" }).notNull().default(false),
  createdAt: integer("created_at", { mode: "timestamp" }).notNull().default(sql`(unixepoch())`),
});

interface DB {
  notes: { id: Generated<number>; title: string; body: string | null; done: Generated<number>; created_at: Generated<number> };
}

export function open() {
  const sqlite = new Database(process.env.DB_FILE ?? "notes.db");
  sqlite.pragma("journal_mode = WAL");
  const db = drizzle(sqlite, { schema: { notes } });
  db.run(sql`CREATE TABLE IF NOT EXISTS notes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    title TEXT NOT NULL,
    body TEXT,
    done INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
  )`);
  const kysely = new Kysely<DB>({ dialect: new SqliteDialect({ database: sqlite }) });
  return { sqlite, db, kysely };
}
