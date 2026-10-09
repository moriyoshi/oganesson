import createMDX from "@next/mdx";
import path from "node:path";
import { fileURLToPath } from "node:url";

const dir = path.dirname(fileURLToPath(import.meta.url));
const withMDX = createMDX({});

/** @type {import('next').NextConfig} */
const nextConfig = {
  pageExtensions: ["js", "jsx", "md", "mdx", "ts", "tsx"],
  webpack(config, { isServer, webpack }) {
    // @svgr/webpack as Next's documentation sets it up: .svg imports are
    // components, ?url ones stay Next's image imports.
    const fileLoaderRule = config.module.rules.find((rule) => rule.test?.test?.(".svg"));
    config.module.rules.push(
      { ...fileLoaderRule, test: /\.svg$/i, resourceQuery: /url/ },
      {
        test: /\.svg$/i,
        issuer: fileLoaderRule.issuer,
        resourceQuery: { not: [...fileLoaderRule.resourceQuery.not, /url/] },
        use: ["@svgr/webpack"],
      },
    );
    fileLoaderRule.exclude = /\.svg$/i;
    // A loader of the project's own, as rule.loader with rule.options.
    config.module.rules.push({ test: /\.txt$/, loader: path.join(dir, "lib/txt-loader.cjs"), options: { suffix: "!" } });
    // .wasm imports as ES modules (app/wasm).
    config.experiments = { ...config.experiments, asyncWebAssembly: true };
    // A loader resolving names with getResolve({ preferRelative: true }).
    config.module.rules.push({ test: /\.links$/, use: [path.join(dir, "lib/links-loader.cjs")] });
    // An alias, a definition per target, and a browser fallback.
    config.resolve.alias["@greeting"] = path.join(dir, "lib/greeting.ts");
    config.plugins.push(new webpack.DefinePlugin({ "process.env.BUILT_FOR": JSON.stringify(isServer ? "server" : "client") }));
    if (!isServer) config.resolve.fallback = { ...config.resolve.fallback, "og-fixture-node-only": false };
    return config;
  },
};

export default withMDX(nextConfig);
