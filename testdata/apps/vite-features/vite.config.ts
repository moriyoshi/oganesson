import { defineConfig, type Plugin } from "vite";

// A plugin that parses modules with this.parse (Rollup's acorn-compatible
// parser) and exposes what it found as a virtual module.
function importCounter(): Plugin {
  const counts: Record<string, number> = {};
  return {
    name: "import-counter",
    resolveId: (id) => (id === "virtual:import-counts" ? "\0virtual:import-counts" : null),
    load(id) {
      if (id === "\0virtual:import-counts") return `export default ${JSON.stringify(counts)};`;
    },
    transform(code, id) {
      if (!id.endsWith("/src/main.ts")) return;
      const ast = this.parse(code);
      counts.main = ast.body.filter((n: { type: string }) => n.type === "ImportDeclaration").length;
    },
  };
}

export default defineConfig({
  plugins: [importCounter()],
  build: {
    manifest: true,
    sourcemap: true,
    assetsInlineLimit: 0,
    rollupOptions: {
      output: {
        assetFileNames: (info) => (info.names?.[0]?.endsWith(".txt") ? "text/[name]-[hash][extname]" : "assets/[name]-[hash][extname]"),
      },
    },
  },
});
