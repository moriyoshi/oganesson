// Prisma 7: the generated TypeScript client (prisma-client generator) with
// its WebAssembly query compiler, over the better-sqlite3 driver adapter.
import { PrismaClient } from "@/generated/prisma/client";
import { PrismaBetterSqlite3 } from "@prisma/adapter-better-sqlite3";

export const dynamic = "force-dynamic";

const SCHEMA = [
  `DROP TABLE IF EXISTS "Post"`,
  `DROP TABLE IF EXISTS "User"`,
  `CREATE TABLE "User" ("id" INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, "email" TEXT NOT NULL, "name" TEXT, "created" DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
  `CREATE UNIQUE INDEX "User_email_key" ON "User"("email")`,
  `CREATE TABLE "Post" ("id" INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, "title" TEXT NOT NULL, "published" BOOLEAN NOT NULL DEFAULT false, "views" INTEGER NOT NULL DEFAULT 0, "authorId" INTEGER NOT NULL, CONSTRAINT "Post_authorId_fkey" FOREIGN KEY ("authorId") REFERENCES "User" ("id") ON DELETE RESTRICT ON UPDATE CASCADE)`,
];

export async function GET() {
  const adapter = new PrismaBetterSqlite3({ url: process.env.PRISMA_DB_FILE ?? "file:./prisma.db" });
  const prisma = new PrismaClient({ adapter });
  try {
    for (const s of SCHEMA) await prisma.$executeRawUnsafe(s);
    const alice = await prisma.user.create({
      data: { email: "alice@example.com", name: "Alice", posts: { create: [{ title: "Hello", published: true, views: 3 }, { title: "Draft" }] } },
      include: { posts: { orderBy: { id: "asc" } } },
    });
    await prisma.user.create({ data: { email: "bob@example.com" } });
    const published = await prisma.post.findMany({ where: { published: true }, select: { title: true, author: { select: { email: true } } } });
    const counts = await prisma.user.findMany({ orderBy: { id: "asc" }, select: { email: true, _count: { select: { posts: true } } } });
    const agg = await prisma.post.aggregate({ _sum: { views: true }, _count: true });
    let rollback = "";
    try {
      await prisma.$transaction(async (tx) => {
        await tx.post.deleteMany({});
        throw new Error("abort");
      });
    } catch (e) {
      rollback = (e as Error).message;
    }
    let unique = "";
    try {
      await prisma.user.create({ data: { email: "alice@example.com" } });
    } catch (e) {
      unique = (e as { code?: string }).code ?? "";
    }
    const raw = await prisma.$queryRaw<{ n: bigint }[]>`SELECT count(*) AS n FROM "Post"`;
    return Response.json({
      alice: { name: alice.name, posts: alice.posts.map((p) => [p.title, p.published, p.views]), created: alice.created instanceof Date },
      published,
      counts,
      agg,
      rollback,
      unique,
      posts: Number(raw[0].n),
    });
  } finally {
    await prisma.$disconnect();
  }
}
