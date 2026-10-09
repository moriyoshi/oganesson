// Intl.Collator (ECMA-402) over the host's CLDR collation (collator.go),
// and String.prototype.localeCompare through it, as Node's ICU does.
import { host } from "./host";

const SENSITIVITIES = ["base", "accent", "case", "variant"];

function localeList(locales: unknown): string[] {
  if (locales === undefined) return [];
  return (typeof locales === "string" ? [locales] : Array.from(locales as Iterable<unknown>)).map(String);
}

function option(options: any, name: string, allowed: string[] | null, fallback: string): string {
  const v = options[name];
  if (v === undefined) return fallback;
  const s = String(v);
  if (allowed && !allowed.includes(s)) throw new RangeError(`Value ${s} out of range for Intl.Collator options property ${name}`);
  return s;
}

export class Collator {
  #impl: { locale: string; compare(a: string, b: string): number };
  #options: { usage: string; sensitivity: string; ignorePunctuation: boolean; collation: string; numeric: boolean; caseFirst: string };
  #bound?: (a: unknown, b: unknown) => number;

  constructor(locales?: unknown, options?: any) {
    const list = localeList(locales);
    options = options === undefined ? {} : Object(options);
    const usage = option(options, "usage", ["sort", "search"], "sort");
    const sensitivity = option(options, "sensitivity", SENSITIVITIES, "variant");
    // -u-kn in the locale turns numeric collation on unless the option says.
    const kn = list.some((l) => /-u(?:-[a-z0-9]{2,8})*-kn(?:-true)?(?:-|$)/i.test(l));
    const numeric = options.numeric === undefined ? kn : !!options.numeric;
    this.#impl = host.collator(list, sensitivity, numeric);
    this.#options = {
      usage, sensitivity, ignorePunctuation: !!options.ignorePunctuation, collation: "default", numeric,
      caseFirst: option(options, "caseFirst", ["upper", "lower", "false"], "false"),
    };
  }

  get compare(): (a: unknown, b: unknown) => number {
    return (this.#bound ??= (a: unknown, b: unknown) => this.#impl.compare(String(a), String(b)));
  }

  resolvedOptions() {
    return { locale: this.#impl.locale, ...this.#options };
  }

  static supportedLocalesOf(locales?: unknown): string[] {
    return localeList(locales).filter((l) => host.collator([l], "variant", false).locale.toLowerCase().split("-")[0] === l.toLowerCase().split("-")[0]);
  }
}
Object.defineProperty(Collator.prototype, Symbol.toStringTag, { value: "Intl.Collator", configurable: true });

let defaultCollator: Collator | undefined;

export function installCollator(intl: any) {
  Object.defineProperty(intl, "Collator", { value: Collator, writable: true, configurable: true, enumerable: false });
  Object.defineProperty(String.prototype, "localeCompare", {
    value: function localeCompare(this: unknown, that: unknown, locales?: unknown, options?: unknown) {
      if (this === null || this === undefined) throw new TypeError("String.prototype.localeCompare called on null or undefined");
      const c = locales === undefined && options === undefined ? (defaultCollator ??= new Collator()) : new Collator(locales, options);
      return c.compare(String(this), String(that));
    },
    writable: true,
    configurable: true,
    enumerable: false,
  });
}
