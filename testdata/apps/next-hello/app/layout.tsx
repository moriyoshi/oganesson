import type { Metadata } from "next";
import Counter from "./counter";
import "./globals.css";
import StyledJsxRegistry from "./styled-registry";

export const metadata: Metadata = {
  title: "next-hello",
  description: "Oganesson fixture",
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body>
        <StyledJsxRegistry>
          <header>Oganesson fixture</header>
          <Counter />
          <main>{children}</main>
        </StyledJsxRegistry>
      </body>
    </html>
  );
}
