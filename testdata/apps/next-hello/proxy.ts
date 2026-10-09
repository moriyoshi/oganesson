import { NextResponse, type NextRequest } from "next/server";

export function proxy(request: NextRequest) {
  const { pathname } = request.nextUrl;
  if (pathname === "/old") {
    return NextResponse.redirect(new URL("/", request.url));
  }
  if (pathname === "/blocked") {
    return new NextResponse("blocked by proxy", { status: 403 });
  }
  if (pathname === "/hello-rewrite") {
    const res = NextResponse.rewrite(new URL("/blog/hello", request.url));
    res.headers.set("x-proxied", "rewrite");
    return res;
  }
  const headers = new Headers(request.headers);
  headers.set("x-from-proxy", "yes");
  const res = NextResponse.next({ request: { headers } });
  res.headers.set("x-proxied", "next");
  res.cookies.set("seen", "1");
  return res;
}

export const config = {
  matcher: ["/((?!_next/static|favicon.ico).*)"],
};
