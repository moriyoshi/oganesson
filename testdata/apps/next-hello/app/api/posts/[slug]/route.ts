import { NextResponse, type NextRequest } from "next/server";
import { headers } from "next/headers";
import { getPost } from "@/lib/posts";

export async function GET(_req: NextRequest, { params }: { params: Promise<{ slug: string }> }) {
  const { slug } = await params;
  const post = await getPost(slug);
  if (!post) return NextResponse.json({ error: "not found" }, { status: 404 });
  const ua = (await headers()).get("user-agent");
  return NextResponse.json({ ...post, ua }, { headers: { "x-post": slug } });
}
