// node:assert: unenv's assert, as Node's module is: the assert function
// itself, with its methods (require("assert")(cond) in CommonJS code).
"use strict";
module.exports = require("unenv:node/assert").default;
