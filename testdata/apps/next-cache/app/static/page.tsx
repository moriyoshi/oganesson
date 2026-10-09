import { stamp } from "../../lib/data";

// No dynamic data: prerendered whole at build time.
export default async function Static() {
  return <p id="static-stamp">{await stamp("static")}</p>;
}
