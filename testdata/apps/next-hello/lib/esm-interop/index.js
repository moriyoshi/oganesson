// An ES module ("type": "module"): its default import of a CommonJS module
// is module.exports, as Node gives it (tslib's modules/index.js).
import flagged from "./flagged.cjs";
import * as ns from "./flagged.cjs";
import { brand } from "./font.js";

export const interop = { defaultHello: flagged.hello, namespaceHello: ns.hello, fontVariable: brand.variable.startsWith("__variable_") };
