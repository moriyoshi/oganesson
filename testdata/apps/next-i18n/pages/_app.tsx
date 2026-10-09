import type { AppProps } from "next/app";
import Link from "next/link";
import { useRouter } from "next/router";

export default function App({ Component, pageProps }: AppProps) {
  const { locale, locales, asPath } = useRouter();
  return (
    <>
      <header>
        locale <span id="locale">{locale}</span> ·{" "}
        {locales?.map((l) => (
          <Link key={l} href={asPath} locale={l} id={`to-${l}`}>
            {l}{" "}
          </Link>
        ))}
        <Link href="/about" id="to-about">about</Link> · <Link href="/posts/1" id="to-post">post 1</Link>
      </header>
      <Component {...pageProps} />
    </>
  );
}
