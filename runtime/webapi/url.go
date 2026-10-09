package webapi

import (
	"strconv"

	"github.com/grafana/sobek"
	whatwg "github.com/nlnwa/whatwg-url/url"
)

var urlParser = whatwg.NewParser()

func (h *host) installURL() {
	// urlParse(input, base?) -> record | null
	h.set("urlParse", func(fc sobek.FunctionCall) sobek.Value {
		input := fc.Argument(0).String()
		var (
			u   *whatwg.Url
			err error
		)
		if base := fc.Argument(1); sobek.IsUndefined(base) {
			u, err = urlParser.Parse(input)
		} else {
			u, err = urlParser.ParseRef(base.String(), input)
		}
		if err != nil {
			return sobek.Null()
		}
		return h.urlRecord(u)
	})
	// urlSet(href, component, value) -> record. Invalid values are ignored,
	// exactly as the URL setters specify.
	h.set("urlSet", func(href, component, value string) sobek.Value {
		u, err := urlParser.Parse(href)
		if err != nil {
			return sobek.Null()
		}
		switch component {
		case "protocol":
			u.SetProtocol(value)
		case "username":
			u.SetUsername(value)
		case "password":
			u.SetPassword(value)
		case "host":
			u.SetHost(value)
		case "hostname":
			u.SetHostname(value)
		case "port":
			u.SetPort(value)
		case "pathname":
			u.SetPathname(value)
		case "search":
			u.SetSearch(value)
		case "hash":
			u.SetHash(value)
		}
		return h.urlRecord(u)
	})
}

func (h *host) urlRecord(u *whatwg.Url) sobek.Value {
	o := h.vm.NewObject()
	_ = o.Set("href", u.Href(false))
	_ = o.Set("origin", origin(u))
	_ = o.Set("protocol", u.Protocol())
	_ = o.Set("username", u.Username())
	_ = o.Set("password", u.Password())
	_ = o.Set("host", u.Host())
	_ = o.Set("hostname", u.Hostname())
	_ = o.Set("port", u.Port())
	_ = o.Set("pathname", u.Pathname())
	_ = o.Set("search", u.Search())
	_ = o.Set("hash", u.Hash())
	return o
}

// origin serializes a URL's origin per the URL Standard.
func origin(u *whatwg.Url) string {
	switch scheme := u.Scheme(); scheme {
	case "http", "https", "ws", "wss", "ftp":
		s := scheme + "://" + u.Hostname()
		if p := u.Port(); p != "" {
			if _, err := strconv.Atoi(p); err == nil {
				s += ":" + p
			}
		}
		return s
	case "blob":
		inner, err := urlParser.Parse(u.Pathname())
		if err == nil && (inner.Scheme() == "http" || inner.Scheme() == "https") {
			return origin(inner)
		}
	}
	return "null"
}
