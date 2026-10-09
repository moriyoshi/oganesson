import { useRouter } from "next/router";

export default function NotFound() {
  return <h1 id="nf">404 ({useRouter().locale})</h1>;
}
