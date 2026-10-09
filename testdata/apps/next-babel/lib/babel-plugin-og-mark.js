// A Babel plugin of the project's own: __BABEL_MARK__ becomes a string
// naming the module it was compiled in. It only takes effect when Next
// compiles with Babel (a .babelrc), as next build then does.
const path = require("path");

module.exports = function ogMark({ types: t }) {
  return {
    visitor: {
      Identifier(p, state) {
        if (p.node.name === "__BABEL_MARK__" && !p.parentPath.isMemberExpression({ property: p.node })) {
          p.replaceWith(t.stringLiteral(`compiled by babel: ${path.basename(state.filename)}`));
        }
      },
    },
  };
};
