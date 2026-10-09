// A prebuild generator (npm runs "prebuild" before "build"): writes a module
// the app imports, from its arguments and environment.
const fs = require("fs");
const path = require("path");

const [label] = process.argv.slice(2);
const out = path.join(__dirname, "..", "lib", "build-info.generated.js");
fs.writeFileSync(out, `export const buildInfo = ${JSON.stringify({ label, mode: process.env.GEN_MODE })};\n`);
console.log("wrote", path.relative(process.cwd(), out));
