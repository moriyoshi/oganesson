import type { NextConfig } from "next";

// i18n domain routing: each domain has its own default locale. The locale
// redirect for "/" uses the request host to pick the domain, and redirects
// across domains when the preferred locale belongs to a different one.
const nextConfig: NextConfig = {
  i18n: {
    locales: ["en", "fr", "nl-NL"],
    defaultLocale: "en",
    domains: [
      { domain: "example.fr", defaultLocale: "fr" },
      { domain: "example.nl", defaultLocale: "nl-NL" },
    ],
  },
};

export default nextConfig;
