export async function GET(request: Request) {
  const url = new URL(request.url);
  return Response.json({ hello: url.searchParams.get("name") ?? "world", runtime: process.env.NEXT_RUNTIME });
}

export async function POST(request: Request) {
  const body = await request.json();
  return Response.json({ received: body }, { status: 201 });
}
