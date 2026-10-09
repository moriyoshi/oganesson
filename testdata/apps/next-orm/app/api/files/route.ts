// The runtime's filesystem: packaged files under an in-memory layer that
// takes writes, shared by requests. GET reports; POST writes.
import fs from "node:fs";

export const dynamic = "force-dynamic";

export function GET() {
  const db = process.env.MIGRATE_DB_FILE ?? "migrate.db";
  const head = fs.existsSync(db) ? fs.readFileSync(db).subarray(0, 15).toString("latin1") : null;
  return Response.json({
    uploads: fs.existsSync("uploads") ? fs.readdirSync("uploads").sort() : null,
    note: fs.existsSync("uploads/b.txt") ? fs.readFileSync("uploads/b.txt", "utf8") : null,
    hello: fs.readFileSync("content/hello.md", "utf8").split("\n")[0],
    db: head,
  });
}

export function POST() {
  fs.mkdirSync("uploads/sub", { recursive: true });
  fs.writeFileSync("uploads/a.txt", "one");
  fs.appendFileSync("uploads/a.txt", "+two");
  fs.renameSync("uploads/a.txt", "uploads/b.txt");
  fs.writeFileSync("uploads/sub/c.txt", "c");
  fs.rmSync("uploads/sub", { recursive: true });
  fs.writeFileSync("content/hello.md", "# Changed\n"); // over a packaged file
  let err = "";
  try { fs.rmdirSync("content-missing"); } catch (e) { err = (e as NodeJS.ErrnoException).code ?? ""; }
  return Response.json({ ok: true, err });
}
