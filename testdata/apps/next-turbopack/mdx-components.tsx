import type { MDXComponents } from "mdx/types";

// @next/mdx's components, through Turbopack's mdx-import-source: headings
// get a marker class.
export function useMDXComponents(components: MDXComponents): MDXComponents {
  return {
    h1: ({ children }) => <h1 className="turbo-mdx-heading">{children}</h1>,
    ...components,
  };
}
