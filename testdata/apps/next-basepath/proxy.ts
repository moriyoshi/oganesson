import { NextResponse, type NextRequest } from "next/server";

// Guards a path under the basePath. Next's getMiddlewareMatchers prepends the
// basePath ("/docs") to the matcher source, so this only runs for
// /docs/guarded*, never for /guarded. It lets TestBasePath check that og
// builds the matcher regexp with the basePath.
export function proxy(_request: NextRequest) {
  return new NextResponse("guarded by proxy", { status: 403 });
}

export const config = {
  matcher: ["/guarded/:path*"],
};
