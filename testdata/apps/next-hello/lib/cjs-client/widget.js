"use strict";
'use client';
// A CommonJS client module, as TypeScript compiles @next/third-parties'.
Object.defineProperty(exports, "__esModule", { value: true });
exports.Widget = void 0;
const jsx_runtime_1 = require("react/jsx-runtime");
const react_1 = require("react");
function Widget() {
  const [n] = (0, react_1.useState)(9);
  return (0, jsx_runtime_1.jsx)("b", { id: "cjs-client", children: "cjs client " + n });
}
exports.Widget = Widget;
