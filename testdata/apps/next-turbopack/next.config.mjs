import createMDX from "@next/mdx";
import path from "node:path";
import { fileURLToPath } from "node:url";

const dir = path.dirname(fileURLToPath(import.meta.url));
const withMDX = createMDX({});

// next build (no --webpack) bundles with Turbopack: the turbopack section
// applies and webpack() is ignored. @next/mdx sees TURBOPACK set and adds
// its Turbopack rule for .mdx, aliasing its import source to Turbopack's
// mdx-import-source (the project's mdx-components).
/** @type {import('next').NextConfig} */
const nextConfig = {
  pageExtensions: ["js", "jsx", "md", "mdx", "ts", "tsx"],
  turbopack: {
    rules: {
      "*.upper.data": { loaders: [path.join(dir, "lib/upper-loader.cjs")], as: "*.js", condition: { not: "foreign" } },
      "*.tcss": { loaders: [{ loader: path.join(dir, "lib/tcss-loader.cjs"), options: { color: "rgb(1, 2, 3)" } }], as: "*.css" },
    },
    resolveAlias: { "@turbo-greeting": path.join(dir, "lib/turbo-greeting.ts") },
  },
  // Ignored under Turbopack: the page would show "webpack" if it applied.
  webpack(config, { webpack }) {
    config.plugins.push(new webpack.DefinePlugin({ "process.env.OG_FIXTURE_BUNDLER": JSON.stringify("webpack") }));
    return config;
  },
};

export default withMDX(nextConfig);
