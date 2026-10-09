import type { MDXComponents } from "mdx/types";

// @next/mdx's components (next-mdx-import-source-file): headings get a
// marker class.
export function useMDXComponents(components: MDXComponents): MDXComponents {
  return {
    h1: ({ children }) => <h1 className="mdx-heading">{children}</h1>,
    ...components,
  };
}
