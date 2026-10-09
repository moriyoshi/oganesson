// next/font/local in a "type": "module" package (as geist ships its fonts):
// og's font transform compiles the call away.
import localFont from "next/font/local";

export const brand = localFont({ src: "./brand.woff2", variable: "--font-brand" });
