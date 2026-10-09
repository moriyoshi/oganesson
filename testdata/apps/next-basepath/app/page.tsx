import Link from "next/link";
import Image from "next/image";
import dot from "./dot.png";

export default function Home() {
  return (
    <main>
      <h1>basePath home</h1>
      <Link href="/about" id="about-link">About</Link>
      <Image src={dot} alt="dot" id="dot" />
    </main>
  );
}
