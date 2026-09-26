package torge

import (
	"maps"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Request/response conveniences in the spirit of Express's `req`/`res`
// helpers (`send`, `format`, `accepts`, `is`, `location`, `type`, `links`,
// cookie and host helpers). Torge already covers the fundamentals with
// stronger guarantees (spoof-safe RealIP, validated binding, typed
// handlers); this file closes the remaining ergonomic gaps without changing
// any existing behavior. Everything here is stdlib-only and additive.

// Request helpers.

// Hostname returns the request host without the port, lowercased. A bare
// IPv6 host keeps no brackets: "[::1]" and "::1" both yield "::1".
func (c *Context) Hostname() string {
	h := c.req.Host
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	} else {
		h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// Secure reports whether the request used TLS (directly or, from a trusted
// proxy, via X-Forwarded-Proto).
func (c *Context) Secure() bool { return c.Scheme() == "https" }

// XHR reports whether the request was made by XMLHttpRequest.
func (c *Context) XHR() bool { return c.GetHeader("X-Requested-With") == "XMLHttpRequest" }

// Is reports whether the request Content-Type matches any of types. Each
// entry is a MIME type ("application/json") or a shorthand ("json"), which
// also matches structured-syntax suffixes ("application/hal+json").
func (c *Context) Is(types ...string) bool {
	candidate := strings.ToLower(baseMediaType(c.req.Header.Get("Content-Type")))
	if candidate == "" {
		return false
	}
	for _, t := range types {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if strings.Contains(t, "/") {
			if candidate == t {
				return true
			}
			continue
		}
		if candidate == expandMediaShorthand(t) || strings.HasSuffix(candidate, "+"+t) {
			return true
		}
	}
	return false
}

// Accepts returns the best match for the request Accept header from types,
// or "" when nothing matches. Entries accept MIME types or shorthands
// ("html", "json"). With no Accept header the first type wins; with no
// types the result is "".
func (c *Context) Accepts(types ...string) string {
	if len(types) == 0 {
		return ""
	}
	header := c.req.Header.Get("Accept")
	if strings.TrimSpace(header) == "" {
		return types[0]
	}
	expanded := make([]string, len(types))
	for i, t := range types {
		expanded[i] = expandMediaShorthand(strings.ToLower(strings.TrimSpace(t)))
	}
	if best := negotiateContentType(header, expanded); best >= 0 {
		return types[best]
	}
	return ""
}

// Offer is one response representation Format can serve. Name accepts a
// MIME type or a shorthand ("html", "json").
type Offer struct {
	Name   string
	Handle Handler
}

// Format runs the offer whose name best matches the request Accept header,
// selected per RFC 9110 (most specific range wins, then highest q-value,
// then offer order). The first offer is the default when the client sends
// no Accept header, so list the preferred representation first. A Vary:
// Accept header is set on every response Format writes. When nothing
// matches Format returns a 406 error, so it can be returned directly from
// a handler:
//
//	return c.Format(
//		torge.Offer{Name: "json", Handle: func(c *torge.Context) error {
//			return c.JSON(200, user)
//		}},
//		torge.Offer{Name: "html", Handle: func(c *torge.Context) error {
//			return c.HTML(200, "<h1>hi</h1>")
//		}},
//	)
func (c *Context) Format(offers ...Offer) error {
	if len(offers) == 0 {
		return NotAcceptable(CodeNotAcceptable, "None of the offered media types match the Accept header; available: (nothing)")
	}
	names := make([]string, len(offers))
	for i, o := range offers {
		names[i] = o.Name
	}
	header := c.req.Header.Get("Accept")
	best := 0
	if strings.TrimSpace(header) != "" {
		best = negotiateContentType(header, expandOfferNames(names))
	}
	if best < 0 {
		return NotAcceptable(CodeNotAcceptable, "None of the offered media types match the Accept header; available: "+strings.Join(names, ", "))
	}
	addVary(c, "Accept")
	return offers[best].Handle(c)
}

// Response helpers.

// Location sets the Location response header without changing the status.
// Pair it with Redirect or a 201 Created response.
func (c *Context) Location(url string) { c.Header("Location", url) }

// Links sets an RFC 8288 Link header from relation names to URLs, e.g.
// {"next": "/users?page=3", "last": "/users?page=9"}. Keys are sorted so
// output is deterministic. See SetPageLinks for the paginated shorthand.
func (c *Context) Links(links map[string]string) {
	rels := slices.Sorted(maps.Keys(links))
	parts := make([]string, 0, len(rels))
	for _, rel := range rels {
		parts = append(parts, "<"+links[rel]+`>; rel="`+rel+`"`)
	}
	c.Header("Link", strings.Join(parts, ", "))
}

// Type sets the Content-Type response header from a MIME type or an
// extension/shorthand ("html", "json"). Unknown shorthands fall back to
// application/octet-stream; text types gain a UTF-8 charset.
func (c *Context) Type(t string) {
	ct := strings.TrimSpace(t)
	if !strings.Contains(ct, "/") {
		ct = expandMediaShorthand(strings.ToLower(ct))
	}
	c.Header("Content-Type", withCharset(ct))
}

// Send writes v with status, dispatching on its type: nil writes headers
// only, strings as text/plain, []byte as application/octet-stream, and
// anything else as JSON.
func (c *Context) Send(status int, v any) error {
	switch t := v.(type) {
	case nil:
		return c.Status(status)
	case string:
		return c.String(status, t)
	case []byte:
		return c.Bytes(status, "application/octet-stream", t)
	default:
		return c.JSON(status, v)
	}
}

// ClearCookie expires the named cookie. The path defaults to "/", matching
// the common case for cookies set without an explicit path.
func (c *Context) ClearCookie(name string) {
	c.SetCookie(&http.Cookie{
		Name:    name,
		Value:   "",
		Path:    "/",
		MaxAge:  -1,
		Expires: time.Unix(0, 0).UTC(),
	})
}

// Content negotiation internals.

// mediaShorthands maps short names to MIME types with a fixed table, so
// resolution never depends on OS tables such as the Windows registry or
// /etc/mime.types.
var mediaShorthands = map[string]string{
	"html": "text/html",
	"htm":  "text/html",
	"json": "application/json",
	"xml":  "application/xml",
	"text": "text/plain",
	"txt":  "text/plain",
	"csv":  "text/csv",
	"css":  "text/css",
	"form": "application/x-www-form-urlencoded",
	"bin":  "application/octet-stream",
}

// expandMediaShorthand maps "json" to "application/json" and similar via
// the fixed shorthand table. Inputs containing "/" pass through unchanged,
// as do unknown names (which resolve to application/octet-stream).
func expandMediaShorthand(t string) string {
	if strings.Contains(t, "/") || t == "" {
		return t
	}
	if resolved, ok := mediaShorthands[t]; ok {
		return resolved
	}
	return "application/octet-stream"
}

// expandOfferNames expands offer names for matching while keeping the
// original names for reporting.
func expandOfferNames(names []string) []string {
	expanded := make([]string, len(names))
	for i, n := range names {
		expanded[i] = expandMediaShorthand(strings.ToLower(strings.TrimSpace(n)))
	}
	return expanded
}

// addVary appends value to the Vary response header without duplicating it.
func addVary(c *Context, value string) {
	for _, v := range c.Response().Header().Values("Vary") {
		for part := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return
			}
		}
	}
	h := c.Response().Header()
	if h.Get("Vary") == "" {
		h.Set("Vary", value)
	} else {
		h.Set("Vary", h.Get("Vary")+", "+value)
	}
}

// withCharset adds a UTF-8 charset to text media types lacking parameters.
func withCharset(ct string) string {
	if strings.Contains(ct, ";") {
		return ct
	}
	if strings.HasPrefix(ct, "text/") {
		return ct + "; charset=utf-8"
	}
	return ct
}

type acceptRange struct {
	typ, sub    string
	q           float64
	specificity int
}

// parseAccept parses an Accept header into media ranges in header order.
// Types are lowercased per RFC 9110. Ranges with q=0 are kept: they exclude
// the type rather than merely ranking it last.
func parseAccept(header string) []acceptRange {
	var out []acceptRange
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		segments := strings.Split(part, ";")
		mt := strings.ToLower(baseMediaType(segments[0]))
		typ, sub, ok := strings.Cut(mt, "/")
		if !ok || typ == "" || sub == "" {
			continue
		}
		q := 1.0
		for _, p := range segments[1:] {
			p = strings.TrimSpace(p)
			if k, v, ok := strings.Cut(p, "="); ok && strings.TrimSpace(k) == "q" {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				}
			}
		}
		specificity := 2
		if typ == "*" {
			specificity = 0
		} else if sub == "*" {
			specificity = 1
		}
		out = append(out, acceptRange{typ: typ, sub: sub, q: q, specificity: specificity})
	}
	return out
}

// matchQuality returns the q-value selecting offered type o (an expanded,
// lowercase MIME type): the q of the most specific matching range, or
// -1 when no range matches. A q of 0 excludes the type.
func matchQuality(ranges []acceptRange, o string) float64 {
	typ, sub, ok := strings.Cut(o, "/")
	if !ok {
		return -1
	}
	bestSpec := -1
	q := -1.0
	for _, r := range ranges {
		if (r.typ == "*" || r.typ == typ) && (r.sub == "*" || r.sub == sub) {
			if r.specificity > bestSpec {
				bestSpec = r.specificity
				q = r.q
			}
		}
	}
	return q
}

// negotiateContentType returns the index into offered (already expanded,
// lowercase MIME types) most preferred by the Accept header, or -1. The
// highest q-value wins; ties prefer the earlier offer. Types with q=0 are
// excluded.
func negotiateContentType(header string, offered []string) int {
	ranges := parseAccept(header)
	best, bestQ := -1, 0.0
	for i, o := range offered {
		if q := matchQuality(ranges, o); q > bestQ {
			best, bestQ = i, q
		}
	}
	return best
}
