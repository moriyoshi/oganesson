// A loader that resolves the names its file lists, as @markdoc/next.js
// resolves its schema files: getResolve with preferRelative (a bare name is
// a sibling file first), and a name that does not resolve rejects (a
// package that is not installed, even in the browser build, where og
// stands in for such packages).
const path = require("node:path");

module.exports = async function linksLoader(source) {
  const resolve = this.getResolve({ extensions: [".js"], preferRelative: true });
  const found = {};
  for (const name of String(source).split(/\s+/).filter(Boolean)) {
    try {
      found[name] = path.relative(this.rootContext, await resolve(this.context, name));
    } catch {
      found[name] = null;
    }
  }
  return `export default ${JSON.stringify(found)};`;
};
