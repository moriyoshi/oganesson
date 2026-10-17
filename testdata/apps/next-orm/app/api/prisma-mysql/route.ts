// Prisma 7 over @prisma/adapter-mariadb on og's native mariadb driver shim
// (runtime/natives/js/mariadb.js, over the shared Go MySQL service). See
// TestPrismaMySQL. The result must match the pg client's for the same model and
// operations — Prisma abstracts the database — so a shim fidelity bug diverges.
import { PrismaClient } from "@/generated/prisma-mysql/client";
import { PrismaMariaDb } from "@prisma/adapter-mariadb";

export const dynamic = "force-dynamic";

export async function GET() {
  const adapter = new PrismaMariaDb({ uri: process.env.PRISMA_MYSQL_URL!, connectionLimit: 2 });
  const prisma = new PrismaClient({ adapter });
  try {
    await prisma.$executeRawUnsafe("DROP TABLE IF EXISTS `Widget`");
    await prisma.$executeRawUnsafe(
      "CREATE TABLE `Widget` (`id` INT AUTO_INCREMENT PRIMARY KEY, `name` VARCHAR(191) NOT NULL, `active` BOOLEAN NOT NULL DEFAULT FALSE, `qty` INT NOT NULL DEFAULT 0)",
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
