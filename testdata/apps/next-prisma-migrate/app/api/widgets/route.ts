// Reads the Widget table through Prisma (adapter-better-sqlite3). The table and
// its seed rows exist only if `migrate deploy` ran at app startup — this route
// never creates them. If migration did not run, prisma.widget.findMany throws.
import { PrismaClient } from "@/generated/prisma/client";
import { PrismaBetterSqlite3 } from "@prisma/adapter-better-sqlite3";

export const dynamic = "force-dynamic";

export async function GET() {
  const adapter = new PrismaBetterSqlite3({ url: process.env.PRISMA_DB_FILE ?? "file:./migrate.db" });
  const prisma = new PrismaClient({ adapter });
  try {
    const widgets = await prisma.widget.findMany({ orderBy: { id: "asc" } });
    return Response.json({ widgets });
  } finally {
    await prisma.$disconnect();
  }
}
