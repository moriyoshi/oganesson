import { desc, eq, count } from "drizzle-orm";
import { open, notes } from "@/lib/db";
import { addNote } from "./actions";

export const dynamic = "force-dynamic";

export default async function Page() {
  const { db, kysely, sqlite } = open();
  try {
    // Drizzle: the relational query API and the SQL-like builder.
    const rows = await db.query.notes.findMany({ orderBy: [desc(notes.id)] });
    const [{ open: pending }] = db.select({ open: count() }).from(notes).where(eq(notes.done, false)).all();
    // Kysely over the same connection.
    const titles = await kysely.selectFrom("notes").select("title").orderBy("id").execute();
    return (
      <main>
        <h1>notes</h1>
        <p id="pending">{pending} open</p>
        <ul id="drizzle">
          {rows.map((n) => (
            <li key={n.id} data-done={String(n.done)} data-created={n.createdAt instanceof Date ? "date" : typeof n.createdAt}>
              {n.title}
              {n.body ? `: ${n.body}` : ""}
            </li>
          ))}
        </ul>
        <p id="kysely">{titles.map((t) => t.title).join(", ")}</p>
        <form action={addNote}>
          <input name="title" />
          <input name="body" />
          <button type="submit">add</button>
        </form>
      </main>
    );
  } finally {
    sqlite.close();
  }
}
