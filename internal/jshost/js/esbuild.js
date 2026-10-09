// A stand-in for the `esbuild` npm package inside the plugin host (the real
// one ships a native binary). transform/formatMessages are backed by the
// esbuild Go API that Oganesson embeds; build/context are not available.
const g = globalThis[Symbol.for("og.pluginEsbuild")];
const unsupported = (name) => () => { throw new Error(`og: esbuild.${name}() is not available inside plugins`); };

module.exports = {
  version: g.version,
  transform: async (input, options) => g.transform(String(input), options || {}),
  transformSync: (input, options) => g.transform(String(input), options || {}),
  formatMessages: async (messages) => messages.map((m) => (typeof m === "string" ? m : m.text)),
  formatMessagesSync: (messages) => messages.map((m) => (typeof m === "string" ? m : m.text)),
  build: unsupported("build"),
  buildSync: unsupported("buildSync"),
  context: unsupported("context"),
  analyzeMetafile: async (m) => (typeof m === "string" ? m : JSON.stringify(m)),
};
