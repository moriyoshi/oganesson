// Reads the Widget table through Prisma over @prisma/adapter-pg. The table and
// its seed rows exist only if `migrate deploy` ran at app startup against the
// same PostgreSQL datasource (DATABASE_URL); this route never creates them.
import { PrismaClient } from "@/generated/prisma/client";
import { PrismaPg } from "@prisma/adapter-pg";

export const dynamic = "force-dynamic";

export async function GET() {
  const adapter = new PrismaPg({ connectionString: process.env.DATABASE_URL!, max: 1 });
  const prisma = new PrismaClient({ adapter });
  try {
    const widgets = await prisma.widget.findMany({ orderBy: { id: "asc" } });
    return Response.json({ widgets });
  } finally {
    await prisma.$disconnect();
  }
}
