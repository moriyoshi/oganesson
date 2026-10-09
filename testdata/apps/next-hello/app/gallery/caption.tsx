"use client";

import localFont from "next/font/local";

const geistBold = localFont({ src: "./fonts/geist-latin.woff2", weight: "700", preload: false });

export default function Caption({ children }: { children: React.ReactNode }) {
  return (
    <figcaption id="caption" className={geistBold.className}>
      {children}
    </figcaption>
  );
}
