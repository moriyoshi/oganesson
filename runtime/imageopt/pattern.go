package imageopt

import (
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// hasLocalMatch ports next/dist/shared/lib/match-local-pattern.
func hasLocalMatch(patterns []LocalPattern, pathAndQuery string) bool {
	if patterns == nil {
		return true // no localPatterns: every local image is allowed
	}
	u, err := url.Parse("http://n" + pathAndQuery)
	if err != nil {
		return false
	}
	for _, p := range patterns {
		if p.Search != nil && *p.Search != search(u) {
			continue
		}
		if globRe(deref(p.Pathname, "**")).MatchString(u.EscapedPath()) {
			return true
		}
	}
	return false
}

// hasRemoteMatch ports next/dist/shared/lib/match-remote-pattern.
func hasRemoteMatch(domains []string, patterns []RemotePattern, u *url.URL) bool {
	for _, d := range domains {
		if u.Hostname() == d {
			return true
		}
	}
	for _, p := range patterns {
		if p.Protocol != nil && strings.TrimSuffix(*p.Protocol, ":") != u.Scheme {
			continue
		}
		if p.Port != nil && *p.Port != u.Port() {
			continue
		}
		if p.Hostname == nil || !globRe(*p.Hostname).MatchString(u.Hostname()) {
			continue
		}
		if p.Search != nil && *p.Search != search(u) {
			continue
		}
		if globRe(deref(p.Pathname, "**")).MatchString(u.EscapedPath()) {
			return true
		}
	}
	return false
}

// search is WHATWG URL's search: "" or "?query".
func search(u *url.URL) string {
	if u.RawQuery == "" {
		return ""
	}
	return "?" + u.RawQuery
}

func deref(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}

var globCache sync.Map

// globRe compiles the subset of picomatch's makeRe that image patterns use:
// "**" spans path segments (a trailing "/**" also matches the bare
// prefix), "*" matches within a segment, "?" one character, and "{a,b}"
// alternatives.
func globRe(glob string) *regexp.Regexp {
	if re, ok := globCache.Load(glob); ok {
		return re.(*regexp.Regexp)
	}
	var b strings.Builder
	b.WriteString("^")
	depth := 0
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case strings.HasPrefix(glob[i:], "/**") && (i+3 == len(glob) || glob[i+3] == '/'):
			b.WriteString(`(?:/.*)?`) // for "/**/", the next "/" follows
			i += 2
		case strings.HasPrefix(glob[i:], "**/") && i == 0:
			b.WriteString(`(?:.*/)?`)
			i += 2
		case strings.HasPrefix(glob[i:], "**"):
			b.WriteString(`.*`)
			i++
		case c == '*':
			b.WriteString(`[^/]+`)
		case c == '?':
			b.WriteString(`[^/]`)
		case c == '{':
			b.WriteString(`(?:`)
			depth++
		case c == '}' && depth > 0:
			b.WriteString(`)`)
			depth--
		case c == ',' && depth > 0:
			b.WriteString(`|`)
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		re = regexp.MustCompile(`^` + regexp.QuoteMeta(glob) + `$`)
	}
	globCache.Store(glob, re)
	return re
}
