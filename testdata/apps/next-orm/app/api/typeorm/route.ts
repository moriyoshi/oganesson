// TypeORM over better-sqlite3: schema synchronization, the repository API,
// relations, the query builder and a transaction that rolls back.
import "reflect-metadata";
import { DataSource, MoreThan } from "typeorm";
import { Author, Book } from "@/lib/entities";

export const dynamic = "force-dynamic";

export async function GET() {
  const ds = new DataSource({
    type: "better-sqlite3",
    database: process.env.TYPEORM_DB_FILE ?? "typeorm.db",
    entities: [Author, Book],
    synchronize: true,
    dropSchema: true,
  });
  await ds.initialize();
  try {
    const authors = ds.getRepository(Author);
    const books = ds.getRepository(Book);
    const ursula = await authors.save(authors.create({ name: "Ursula" }));
    await books.save([
      books.create({ title: "A Wizard of Earthsea", pages: 183, available: true, author: ursula }),
      books.create({ title: "The Dispossessed", pages: 387, available: false, author: ursula }),
    ]);
    const withBooks = await authors.findOne({ where: { id: ursula.id }, relations: { books: true }, order: { books: { id: "ASC" } } });
    const long = await books
      .createQueryBuilder("b")
      .innerJoin("b.author", "a")
      .where("b.pages > :n", { n: 200 })
      .select(["b.title AS title", "a.name AS author"])
      .getRawMany();
    let rollback = "no error";
    try {
      await ds.transaction(async (m) => {
        await m.delete(Book, { pages: MoreThan(0) });
        throw new Error("abort");
      });
    } catch (e) {
      rollback = (e as Error).message;
    }
    const columns = ds.getMetadata(Book).columns.map((c) => `${c.propertyName}:${typeof c.type === "function" ? c.type.name : c.type}`);
    return Response.json({
      author: { name: withBooks?.name, bio: withBooks?.bio ?? null, books: withBooks?.books.map((b) => [b.title, b.pages, b.available, b.createdAt instanceof Date]) },
      long,
      rollback,
      count: await books.count(),
      columns,
    });
  } finally {
    await ds.destroy();
  }
}
