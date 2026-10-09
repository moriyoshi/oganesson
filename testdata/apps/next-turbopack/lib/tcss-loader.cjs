// A turbopack rule's loader with CSS output (as: "*.css"): "selector:
// property value" lines become rules; "$color" is the rule's option.
module.exports = function tcssLoader(source) {
  const { color } = this.getOptions();
  return String(source)
    .split("\n")
    .filter((l) => l.includes(":"))
    .map((l) => {
      const [sel, decl] = l.split(":");
      const [prop, ...value] = decl.trim().split(/\s+/);
      return `${sel.trim()} { ${prop}: ${value.join(" ").replace("$color", color)}; }`;
    })
    .join("\n");
};
