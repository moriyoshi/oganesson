// experiments.asyncWebAssembly (next.config's webpack()): calc.wasm (built
// by hand: add_one(x) = x + 1, plus_base(x) = x + base(), with base
// imported from "./base.js") as an ES module, imported statically and
// dynamically.
// @ts-ignore
import { add_one } from "./calc.wasm";

export default async function WasmPage() {
  // @ts-ignore
  const { plus_base } = await import("./calc.wasm");
  return <p id="wasm">{`${add_one(41)} ${plus_base(1)}`}</p>;
}
