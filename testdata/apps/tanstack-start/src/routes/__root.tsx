/// <reference types="vite/client" />
import type { ReactNode } from "react";
import { HeadContent, Link, Outlet, Scripts, createRootRoute } from "@tanstack/react-router";

export const Route = createRootRoute({
  head: () => ({
    meta: [{ charSet: "utf-8" }, { name: "viewport", content: "width=device-width, initial-scale=1" }, { title: "TanStack Start on Oganesson" }],
  }),
  component: RootComponent,
});

function RootComponent() {
  return (
    <RootDocument>
      <nav>
        <Link to="/" id="to-home">home</Link> · <Link to="/about" id="to-about">about</Link> · <Link to="/deferred" id="to-deferred">deferred</Link> · <Link to="/static" id="to-static">static</Link> · <Link to="/iso" id="to-iso">iso</Link> · <Link to="/client-only" id="to-client-only">client-only</Link> · <Link to="/data-only" id="to-data-only">data-only</Link>
      </nav>
      <Outlet />
    </RootDocument>
  );
}

function RootDocument({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <head>
        <HeadContent />
      </head>
      <body>
        {children}
        <Scripts />
      </body>
    </html>
  );
}
