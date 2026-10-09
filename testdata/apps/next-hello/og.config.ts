// og build configuration: run styled-jsx's Babel transform through the
// webpack loader shim (babel-loader), as Next's SWC would.
export default {
  webpack: {
    rules: [
      {
        test: /\.[jt]sx$/,
        exclude: /node_modules/,
        use: [
          {
            loader: "babel-loader",
            options: {
              babelrc: false,
              configFile: false,
              // Only styled-jsx runs; TypeScript and JSX stay for esbuild.
              parserOpts: { plugins: ["jsx", "typescript"] },
              plugins: ["styled-jsx/babel"],
            },
          },
        ],
      },
    ],
  },
};
