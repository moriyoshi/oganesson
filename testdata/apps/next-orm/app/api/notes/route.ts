import { eq, sql } from "drizzle-orm";
import { open, notes } from "@/lib/db";

export const dynamic = "force-dynamic";

// GET: the notes through Kysely (with a computed column and a join of
// aggregates); POST {title, body?, done?}: insert through Drizzle with
// RETURNING; PATCH {id}: mark done in a Drizzle transaction; DELETE: a
// transaction that fails half way and rolls back.
export async function GET() {
  const { kysely, sqlite } = open();
  try {
    const rows = await kysely
      .selectFrom("notes")
      .select(["id", "title", "done", (eb) => eb.fn("length", ["title"]).as("len")])
      .orderBy("id")
      .execute();
    const stats = await kysely.selectFrom("notes").select((eb) => [eb.fn.countAll<number>().as("n"), eb.fn.max("id").as("last")]).executeTakeFirstOrThrow();
    return Response.json({ rows, stats });
  } finally {
    await kysely.destroy();
    if (sqlite.open) sqlite.close();
  }
}

export async function POST(req: Request) {
  const { db, sqlite } = open();
  try {
    const { title, body, done } = await req.json();
    const [row] = db.insert(notes).values({ title, body: body ?? null, done: !!done }).returning({ id: notes.id, title: notes.title, done: notes.done }).all();
    return Response.json(row, { status: 201 });
  } finally {
    sqlite.close();
  }
}

export async function PATCH(req: Request) {
  const { db, sqlite } = open();
  try {
    const { id } = await req.json();
    const updated = db.transaction((tx) => {
      tx.update(notes).set({ done: true }).where(eq(notes.id, id)).run();
      return tx.select().from(notes).where(eq(notes.id, id)).get();
    });
    return Response.json({ id: updated?.id, done: updated?.done });
  } finally {
    sqlite.close();
  }
}

export async function DELETE() {
  const { db, sqlite } = open();
  try {
    let error = "";
    try {
      db.transaction((tx) => {
        tx.delete(notes).run();
        tx.run(sql`INSERT INTO missing_table VALUES (1)`);
      });
    } catch (e) {
      error = (e as Error).message;
    }
    const [{ n }] = db.select({ n: sql<number>`count(*)` }).from(notes).all();
    return Response.json({ rolledBack: n > 0, error: error.includes("missing_table") });
  } finally {
    sqlite.close();
  }
}
