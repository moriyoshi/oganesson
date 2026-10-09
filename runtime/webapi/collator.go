package webapi

import (
	"strings"

	"github.com/grafana/sobek"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// Intl.Collator's comparison (js/src/collator.ts): CLDR collation from
// golang.org/x/text/collate, as ICU does it in Node. Options follow
// ECMA-402 where the library has a counterpart: sensitivity (base: ignore
// case and accents; accent: ignore case; case: ignore accents) and numeric.
// Punctuation is always significant and caseFirst is the locale's.

func (h *host) installCollator() {
	h.set("collator", func(locales []string, sensitivity string, numeric bool) sobek.Value {
		tag := language.Und
		if len(locales) > 0 {
			matcher := language.NewMatcher(collate.Supported())
			if t, _, conf := matcher.Match(parseTags(locales)...); conf != language.No {
				tag = t
			}
		}
		var opts []collate.Option
		switch sensitivity {
		case "base":
			opts = append(opts, collate.IgnoreCase, collate.IgnoreDiacritics)
		case "accent":
			opts = append(opts, collate.IgnoreCase)
		case "case":
			opts = append(opts, collate.IgnoreDiacritics)
		}
		if numeric {
			opts = append(opts, collate.Numeric)
		}
		c := collate.New(tag, opts...)
		o := h.vm.NewObject()
		_ = o.Set("locale", resolvedLocale(tag, locales))
		_ = o.Set("compare", func(a, b string) int { return c.CompareString(a, b) })
		return o
	})
}

func parseTags(locales []string) []language.Tag {
	var tags []language.Tag
	for _, l := range locales {
		if t, err := language.Parse(l); err == nil {
			tags = append(tags, t)
		}
	}
	return tags
}

// resolvedLocale is the requested locale the collator serves (without
// Unicode extensions), as resolvedOptions().locale reports it.
func resolvedLocale(tag language.Tag, locales []string) string {
	for _, l := range locales {
		if t, err := language.Parse(l); err == nil {
			if base, _ := t.Base(); base == mustBase(tag) {
				s := t.String()
				if i := strings.Index(s, "-u-"); i >= 0 {
					s = s[:i]
				}
				return s
			}
		}
	}
	if tag == language.Und {
		return "en-US"
	}
	return tag.String()
}

func mustBase(t language.Tag) language.Base {
	b, _ := t.Base()
	return b
}
