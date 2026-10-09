// The create-next-app shape for Tailwind v4. Oganesson runs it through Next's
// own PostCSS setup (lazyPostCSS) in the build-time JS host; Tailwind's
// native parts (oxide, lightningcss) run as WebAssembly.
const config = {
  plugins: {
    "@tailwindcss/postcss": {},
  },
};

export default config;
