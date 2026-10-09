// Optional packages that are not installed fail where they are loaded, as
// under Node (cosmiconfig's import("typescript"), pg's require("pg-native")).
export async function GET() {
  const out: Record<string, string> = {};
  try {
    await import("og-fixture-not-installed-a");
  } catch (e) {
    out.import = (e as { code: string }).code;
  }
  try {
    require("og-fixture-not-installed-b");
  } catch (e) {
    out.require = (e as { code: string }).code;
  }
  return Response.json(out);
}
