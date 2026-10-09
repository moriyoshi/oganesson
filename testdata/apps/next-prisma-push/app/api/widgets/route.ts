// The Widget table is created by `db push` at app startup (schemaPush), not by
// any migration or runtime DDL. This route inserts one row and reads it back:
// if startup db push did not run, the table would not exist and create() would
// throw. Proves the pushed schema (id autoincrement, name) is live.
import { PrismaClient } from "@/generated/prisma/client";
import { PrismaBetterSqlite3 } from "@prisma/adapter-better-sqlite3";

export const dynamic = "force-dynamic";

export async function GET() {
  const adapter = new PrismaBetterSqlite3({ url: process.env.PRISMA_DB_FILE ?? "file:./push.db" });
  const prisma = new PrismaClient({ adapter });
  try {
    await prisma.widget.create({ data: { name: "pushed" } });
    const widgets = await prisma.widget.findMany({ orderBy: { id: "asc" } });
    return Response.json({ widgets });
  } finally {
    await prisma.$disconnect();
  }
}
