export type Post = { slug: string; title: string; body: string };

const posts: Post[] = [
  { slug: "hello", title: "Hello, Oganesson", body: "This page was rendered by Goja." },
  { slug: "rsc", title: "Server Components", body: "Streamed from a Go binary." },
];

export async function getPosts(): Promise<Post[]> {
  await new Promise((r) => setTimeout(r, 5));
  return posts;
}

export async function getPost(slug: string): Promise<Post | undefined> {
  return (await getPosts()).find((p) => p.slug === slug);
}
