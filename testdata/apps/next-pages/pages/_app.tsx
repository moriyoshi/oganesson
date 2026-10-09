import type { AppProps } from "next/app";
import Link from "next/link";
import "../styles/globals.css";

export default function App({ Component, pageProps }: AppProps) {
  return (
    <>
      <header>
        Pages fixture · <Link href="/">home</Link> · <Link href="/about">about</Link> · <Link href="/blog/hello">blog</Link>
      </header>
      <main>
        <Component {...pageProps} />
      </main>
    </>
  );
}
