// Prisma 7 over @prisma/adapter-pg on og's native pg shim (runtime/natives).
// See TestPrismaPostgres. `max: 1` keeps the adapter to a single connection,
// which single-session PGlite (the test server) can serve.
import { PrismaClient } from "@/generated/prisma-pg/client";
import { PrismaPg } from "@prisma/adapter-pg";

export const dynamic = "force-dynamic";

export async function GET() {
  const adapter = new PrismaPg({ connectionString: process.env.PRISMA_PG_URL!, max: 1 });
  const prisma = new PrismaClient({ adapter });
  try {
    await prisma.$executeRawUnsafe(`DROP TABLE IF EXISTS "Widget"`);
    await prisma.$executeRawUnsafe(
      `CREATE TABLE "Widget" ("id" SERIAL PRIMARY KEY, "name" TEXT NOT NULL, "active" BOOLEAN NOT NULL DEFAULT false, "qty" INTEGER NOT NULL DEFAULT 0)`,
    );
    await prisma.widget.create({ data: { name: "alpha", qty: 3 } });
    await prisma.widget.create({ data: { name: "beta", active: true, qty: 7 } });
    const rows = await prisma.widget.findMany({ orderBy: { id: "asc" } });
    const active = await prisma.widget.findMany({ where: { active: true }, select: { name: true } });
    const agg = await prisma.widget.aggregate({ _sum: { qty: true } });
    let rollback = "";
    try {
      await prisma.$transaction(async (tx) => {
        await tx.widget.deleteMany({});
        throw new Error("abort");
      });
    } catch (e) {
      rollback = (e as Error).message;
    }
    const count = await prisma.widget.count();
    return Response.json({ rows, active, sum: agg._sum.qty, rollback, count });
  } finally {
    await prisma.$disconnect();
  }
}
