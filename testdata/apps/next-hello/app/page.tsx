import Link from "next/link";
import { cookies } from "next/headers";
import { getPosts } from "@/lib/posts";
import { like } from "./actions";
import Shout from "./shout";
import Fancy from "./fancy";

export default async function Home() {
  const posts = await getPosts();
  const likes = (await cookies()).get("likes")?.value ?? "0";
  return (
    <section>
      <h1>Posts</h1>
      <ul>
        {posts.map((p) => (
          <li key={p.slug}>
            <Link href={`/blog/${p.slug}`}>{p.title}</Link>
          </li>
        ))}
      </ul>
      <form action={like}>
        <button id="like" type="submit">like</button> <span id="likes">{likes} likes</span>
      </form>
      <Shout />
      <Fancy />
    </section>
  );
}
