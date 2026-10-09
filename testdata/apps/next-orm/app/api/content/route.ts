// A project file read at request time, through a path Next's file tracing
// follows (process.cwd()).
import { readFileSync, existsSync } from "node:fs";
import path from "node:path";

export const dynamic = "force-dynamic";

export function GET() {
  const file = path.join(process.cwd(), "content", "hello.md");
  return Response.json({ exists: existsSync(file), text: readFileSync(file, "utf8") });
}
