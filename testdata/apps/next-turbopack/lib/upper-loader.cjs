// A turbopack rule's loader with JavaScript output (as: "*.js"): the
// file's text, upper-cased, as the default export.
module.exports = function upperLoader(source) {
  return `export default ${JSON.stringify(String(source).trim().toUpperCase())};`;
};
