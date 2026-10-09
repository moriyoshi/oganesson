import { useRouter } from "next/router";

// Renders the router locale so a next start oracle (and the test) can observe
// which locale a 404 takes under i18n.domains: no locale prefix is in the URL,
// so the locale comes from the request domain.
export default function NotFound() {
  return <h1 id="nf">404 ({useRouter().locale})</h1>;
}
