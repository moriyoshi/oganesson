/// <reference types="vite/client" />
import type { ReactNode } from "react";
import { HeadContent, Link, Outlet, Scripts, createRootRoute } from "@tanstack/react-router";

export const Route = createRootRoute({
  head: () => ({
    meta: [{ charSet: "utf-8" }, { name: "viewport", content: "width=device-width, initial-scale=1" }, { title: "TanStack Start RSC on Oganesson" }],
  }),
  component: RootComponent,
});

function RootComponent() {
  return (
    <html lang="en">
      <head>
        <HeadContent />
      </head>
      <body>
        <nav>
          <Link to="/" id="to-home">home</Link> · <Link to="/other" id="to-other">other</Link> · <Link to="/composite" id="to-composite">composite</Link>
        </nav>
        <Outlet />
        <Scripts />
      </body>
    </html>
  );
}
