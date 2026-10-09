import { revalidateTag } from "next/cache";
import { tagged } from "../../lib/data";

export default async function Tagged() {
  async function refresh() {
    "use server";
    revalidateTag("og-tagged", "max");
  }
  return (
    <main>
      <p id="tagged">{await tagged()}</p>
      <form action={refresh}>
        <button type="submit">refresh</button>
      </form>
    </main>
  );
}
