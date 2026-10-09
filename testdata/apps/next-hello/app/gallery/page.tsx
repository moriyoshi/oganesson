import Image from "next/image";
import localFont from "next/font/local";
import photo from "./photo.jpg";
import Caption from "./caption";
import styles from "./gallery.module.scss";
import "./note.sass";
import badge from "./badge.module.css";
import shared from "../styles/shared.module.css";

// Geist (SIL Open Font License), copied from next/dist/next-devtools.
const geist = localFont({ src: "./fonts/geist-latin.woff2", variable: "--font-geist" });

export const metadata = { title: "Gallery" };

export default function Gallery() {
  return (
    <>
      <p id="font" className={`${geist.className} ${geist.variable}`} data-family={geist.style.fontFamily}>
        Set in Geist.
      </p>
      <Image id="photo" src={photo} alt="A gradient" placeholder="blur" />
      <figure id="sass" className={styles.figure}>
        <span className={styles.inner}>Styled with Sass.</span>
      </figure>
      <p id="sass-note" className="sass-note">Indented syntax.</p>
      <span id="badge" className={badge.badge}>composed</span>
      <span id="pill" className={shared.pill}>shared</span>
      <Caption>Two images</Caption>
      <Image id="square" src="/square.png" alt="A square" width={48} height={48} />
    </>
  );
}
