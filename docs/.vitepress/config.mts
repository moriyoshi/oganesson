import { defineConfig } from "vitepress";

export default defineConfig({
  title: "Oganesson",
  description:
    "Compile a Next.js or Vite application into one self-contained pure-Go executable.",
  lang: "en-US",
  cleanUrls: true,
  // VitePress derives this value by spawning Git. Oganesson deliberately does
  // not expose general child-process execution in its JavaScript build host.
  lastUpdated: false,
  ignoreDeadLinks: false,
  vite: {
    // Reuse the repository's canonical logo assets without copying them into
    // the documentation source tree.
    publicDir: "../assets",
    // Vite 8 defaults to Lightning CSS, whose default package loads a native
    // Node binding. The documentation CSS is small enough to ship unminified.
    build: { cssMinify: false },
  },
  // Markdown-it's automatic bare-URL linkifier currently hits a Sobek
  // RegExp/method-binding incompatibility. All links in these guides are
  // explicit Markdown links, so disabling it does not remove site links.
  markdown: { linkify: false },

  head: [
    ["meta", { name: "theme-color", content: "#161618" }],
    ["meta", { property: "og:type", content: "website" }],
  ],

  themeConfig: {
    siteTitle: "Oganesson",
    logo: "/oganesson-logo.svg",
    nav: [
      { text: "Guide", link: "/introduction" },
      {
        text: "Frameworks",
        items: [
          { text: "Next.js", link: "/nextjs" },
          { text: "Vite", link: "/vite" },
        ],
      },
      { text: "CLI", link: "/cli" },
      {
        text: "GitHub",
        link: "https://github.com/moriyoshi/oganesson",
      },
    ],

    sidebar: [
      {
        text: "Introduction",
        items: [
          { text: "Can I do this?", link: "/can-i-do-this" },
          { text: "Introduction", link: "/introduction" },
          { text: "Getting started", link: "/getting-started" },
        ],
      },
      {
        text: "Using Oganesson",
        items: [
          { text: "Command-line reference", link: "/cli" },
          { text: "Development workflow", link: "/development-workflow" },
          { text: "Project configuration", link: "/configuration" },
          { text: "Packaging and deployment", link: "/deployment" },
        ],
      },
      {
        text: "Frameworks",
        items: [
          { text: "Next.js", link: "/nextjs" },
          { text: "Vite and frameworks", link: "/vite" },
        ],
      },
      {
        text: "Runtime and APIs",
        items: [
          { text: "Runtime compatibility", link: "/runtime" },
          { text: "Data, databases, and ORMs", link: "/data" },
          { text: "Native Go modules", link: "/native-go-modules" },
        ],
      },
      {
        text: "Reference",
        items: [
          { text: "Compatibility and limitations", link: "/compatibility" },
        ],
      },
    ],

    outline: [2, 3],
    search: { provider: "local" },
    editLink: {
      pattern:
        "https://github.com/moriyoshi/oganesson/edit/main/docs/:path",
      text: "Edit this page on GitHub",
    },
    socialLinks: [
      {
        icon: "github",
        link: "https://github.com/moriyoshi/oganesson",
      },
    ],
    footer: {
      message: "Released under the Apache License 2.0.",
    },
    docFooter: {
      prev: "Previous page",
      next: "Next page",
    },
  },
});
