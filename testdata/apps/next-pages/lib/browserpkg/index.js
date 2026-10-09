// A package as bn.js and framer-motion are written: "buffer" disabled in
// browsers by its "browser" field, and an optional package required inside
// a try.
var Buffer;
try {
  Buffer = require("buffer").Buffer;
} catch (e) {}
var optional = "absent";
try {
  optional = require("og-fixture-optional-missing");
} catch (e) {}
exports.describe = function () {
  return (typeof Buffer === "function" ? "buffer" : "no buffer") + ", " + optional;
};
