import postcss from "esbuild-plugin-postcss";

export default {
  esbuild: { clientPlugins: [postcss({ declaration: true })] },
};
