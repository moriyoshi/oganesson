import { interop } from "../../../lib/esm-interop/index.js";

export async function GET() {
  return Response.json(interop);
}
