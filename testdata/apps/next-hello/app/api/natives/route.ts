// Native npm packages, as a Next app uses them; Oganesson substitutes them
// (runtime/natives): bcrypt -> bcryptjs, sharp and better-sqlite3 -> Go.
import bcrypt from "bcrypt";
import sharp from "sharp";
import Database from "better-sqlite3";

// A 30x20 PNG.
const pixel = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAB4AAAAUCAIAAAAVyRqTAAAAHklEQVR4nGNgaPhPKzRq9KjRo0aPGj1q9KjRQ9FoAE9/gdadd/LOAAAAAElFTkSuQmCC", "base64");

export async function GET() {
  const hash = await bcrypt.hash("secret", 4);
  const { info } = await sharp(pixel).resize(15).webp().toBuffer({ resolveWithObject: true });
  const db = new Database(":memory:");
  db.exec("CREATE TABLE notes (body TEXT)");
  db.prepare("INSERT INTO notes VALUES (?)").run("hello");
  const notes = db.prepare("SELECT body FROM notes").pluck().all();
  db.close();
  return Response.json({
    bcrypt: hash.startsWith("$2") && (await bcrypt.compare("secret", hash)) && !(await bcrypt.compare("nope", hash)),
    sharp: `${info.format} ${info.width}x${info.height}`,
    sqlite: notes,
  });
}
