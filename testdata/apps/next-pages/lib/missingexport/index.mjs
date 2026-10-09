// Imports a name the renderer no longer exports: webpack warns and binds
// undefined; the rest of the module works.
import { renderNodeRule, renderRule } from "./renderer.mjs";

export function describeRenderer() {
  return `${renderNodeRule()}, renderRule ${typeof renderRule}`;
}
