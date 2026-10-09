// Intl from FormatJS's polyfills (vendored, see scripts/vendor-js.sh): Goja
// has no Intl at all. Each constructor is an accessor until first use; the
// first access evaluates its polyfill (and the ones it builds on). Request
// VMs that never touch Intl do not pay for FormatJS's data. Locale data is
// English only; DateTimeFormat has the IANA time zone data, and its default
// time zone is TZ (else UTC). The require() paths are literal, so esbuild
// bundles them.
import { defineGlobal } from "./util";

import { installCollator } from "./collator";

declare const require: (id: string) => unknown;

// The polyfill groups, in dependency order; each name loads its group and
// every group before it.
const GROUPS: [string[], () => void][] = [
  [
    ["getCanonicalLocales", "Locale", "Segmenter"],
    () => {
      // Locale needs getCanonicalLocales, and the segmenter's locale
      // matching needs Locale. esbuild evaluates each module (and its
      // dependencies) at its require(), not at prelude start.
      require("../vendor/node_modules/@formatjs/intl-getcanonicallocales/polyfill-force.js");
      require("../vendor/node_modules/@formatjs/intl-locale/polyfill-force.js");
      require("../vendor/node_modules/@formatjs/intl-segmenter/polyfill-force.js");
    },
  ],
  [
    ["PluralRules", "NumberFormat"],
    () => {
      require("../vendor/node_modules/@formatjs/intl-pluralrules/polyfill-force.js");
      require("../vendor/node_modules/@formatjs/intl-pluralrules/locale-data/en.js");
      require("../vendor/node_modules/@formatjs/intl-numberformat/polyfill-force.js");
      require("../vendor/node_modules/@formatjs/intl-numberformat/locale-data/en.js");
    },
  ],
  [
    ["DateTimeFormat"],
    () => {
      require("../vendor/node_modules/@formatjs/intl-datetimeformat/polyfill-force.js");
      // The vendored CLDR data puts U+202F before AM/PM; current CLDR (and
      // Node's ICU) went back to a space, which browsers render too (a
      // difference would be a hydration mismatch).
      const DTF0 = (globalThis as any).Intl.DateTimeFormat;
      const addLocaleData = DTF0.__addLocaleData;
      DTF0.__addLocaleData = (...data: unknown[]) =>
        addLocaleData.apply(DTF0, JSON.parse(JSON.stringify(data).replace(/\u202f/gi, " ")));
      require("../vendor/node_modules/@formatjs/intl-datetimeformat/locale-data/en.js");
      DTF0.__addLocaleData = addLocaleData;
      require("../vendor/node_modules/@formatjs/intl-datetimeformat/add-all-tz.js");
      const tz = (globalThis as any).process?.env?.TZ;
      const DTF = (globalThis as any).Intl.DateTimeFormat;
      if (tz && typeof DTF.__setDefaultTimeZone === "function") {
        try {
          DTF.__setDefaultTimeZone(tz);
        } catch {}
      }
    },
  ],
  [
    ["ListFormat", "RelativeTimeFormat", "DisplayNames"],
    () => {
      require("../vendor/node_modules/@formatjs/intl-listformat/polyfill-force.js");
      require("../vendor/node_modules/@formatjs/intl-listformat/locale-data/en.js");
      require("../vendor/node_modules/@formatjs/intl-listformat/locale-data/en-GB.js");
      require("../vendor/node_modules/@formatjs/intl-relativetimeformat/polyfill-force.js");
      require("../vendor/node_modules/@formatjs/intl-relativetimeformat/locale-data/en.js");
      require("../vendor/node_modules/@formatjs/intl-displaynames/polyfill-force.js");
      require("../vendor/node_modules/@formatjs/intl-displaynames/locale-data/en.js");
    },
  ],
];

let loadedGroups = 0;

function load(intl: any, upTo: number) {
  while (loadedGroups <= upTo) {
    const [names, run] = GROUPS[loadedGroups];
    for (const name of names) delete intl[name];
    loadedGroups++;
    run();
  }
}

export function installIntl() {
  const g = globalThis as any;
  if (typeof g.Intl !== "object" || g.Intl === null) {
    const Intl = {};
    Object.defineProperty(Intl, Symbol.toStringTag, { value: "Intl", configurable: true });
    defineGlobal("Intl", Intl);
  }
  const intl = g.Intl;
  installCollator(intl);
  // The locale-sensitive methods of Number, BigInt and Date format through
  // Intl (ECMA-402; Goja's take the locale for a radix): the polyfills
  // install them when their group loads, which the first call triggers.
  const lazyMethod = (proto: any, name: string, load: () => unknown) => {
    const stub = function (this: unknown, ...args: unknown[]) {
      load();
      const real = proto[name];
      if (real === stub) throw new TypeError(`${name} is not available`);
      return real.apply(this, args);
    };
    Object.defineProperty(proto, name, { value: stub, writable: true, configurable: true, enumerable: false });
  };
  lazyMethod(Number.prototype, "toLocaleString", () => intl.NumberFormat);
  if (typeof BigInt !== "undefined") lazyMethod(BigInt.prototype, "toLocaleString", () => intl.NumberFormat);
  for (const name of ["toLocaleString", "toLocaleDateString", "toLocaleTimeString"]) {
    lazyMethod(Date.prototype, name, () => intl.DateTimeFormat);
  }
  GROUPS.forEach(([names], group) => {
    for (const name of names) {
      if (name in intl) continue;
      Object.defineProperty(intl, name, {
        configurable: true,
        enumerable: false,
        get() {
          load(intl, group);
          return intl[name];
        },
        set(v) {
          Object.defineProperty(intl, name, { value: v, writable: true, configurable: true, enumerable: false });
        },
      });
    }
  });
}
