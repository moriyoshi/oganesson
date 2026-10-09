// A webpack loader of the project's own: text files as string modules. It
// reads its options and the module's buildInfo, as loaders do (nextra
// stores its search data in buildInfo).
module.exports = function txtLoader(source) {
  const { suffix } = this.getOptions();
  this._module.buildInfo.txt = true;
  return `export default ${JSON.stringify(String(source).trim() + suffix + " (" + typeof this._module.buildInfo + ")")};`;
};
