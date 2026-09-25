package torge

import (
	"maps"
	"mime"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Express-style request/response conveniences, implemented the Torge way.
//
// Express applications lean on a small set of `req`/`res` helpers (`send`,
// `format`, `accepts`, `is`, `location`, `type`, `links`, signed-cookie and
// host helpers). Torge already covers the fundamentals with stronger
// guarantees (spoof-safe RealIP, validated binding, typed handlers); this
// file closes the remaining ergonomic gaps without changing any existing
// behavior. Everything here is stdlib-only and additive.

// ---- Request helpers ----

// Hostname returns the request host without the port, lowercased.
func (c *Context) Hostname() string {
	h := c.req.Host
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
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

// Format runs the handler whose key best matches the request Accept header,
// selected by client preference (q-values, then specificity). Keys accept
// MIME types or shorthands. When nothing matches it returns a 406 error, so
// it can be returned directly from a handler:
//
//	return c.Format(map[string]torge.Handler{
//		"html":  func(c *torge.Context) error { return c.HTML(200, "<h1>hi</h1>") },
//		"json":  func(c *torge.Context) error { return c.JSON(200, user) },
//	})
func (c *Context) Format(handlers map[string]Handler) error {
	keys := slices.Sorted(maps.Keys(handlers))
	expanded := make([]string, len(keys))
	for i, k := range keys {
		expanded[i] = expandMediaShorthand(strings.ToLower(strings.TrimSpace(k)))
	}
	header := c.req.Header.Get("Accept")
	best := -1
	if strings.TrimSpace(header) == "" && len(keys) > 0 {
		best = 0
	} else if len(keys) > 0 {
		best = negotiateContentType(header, expanded)
	}
	if best < 0 {
		offered := strings.Join(keys, ", ")
		if offered == "" {
			offered = "(nothing)"
		}
		return NotAcceptable(CodeNotAcceptable, "None of the offered media types match the Accept header; available: "+offered)
	}
	return handlers[keys[best]](c)
}

// ---- Response helpers ----

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

// ---- Content negotiation internals ----

// expandMediaShorthand maps "json" to "application/json" and similar via the
// standard extension table plus a few names without extensions. Inputs
// containing "/" pass through unchanged.
func expandMediaShorthand(t string) string {
	if strings.Contains(t, "/") || t == "" {
		return t
	}
	if resolved := mime.TypeByExtension("." + t); resolved != "" {
		if mt, _, err := mime.ParseMediaType(resolved); err == nil {
			return mt
		}
		return resolved
	}
	switch t {
	case "text":
		return "text/plain"
	case "xml":
		return "application/xml"
	}
	return "application/octet-stream"
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
	order       int
}

func parseAccept(header string) []acceptRange {
	var out []acceptRange
	for i, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		segments := strings.Split(part, ";")
		mt := baseMediaType(segments[0])
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
		if q <= 0 {
			continue
		}
		specificity := 2
		if typ == "*" {
			specificity = 0
		} else if sub == "*" {
			specificity = 1
		}
		out = append(out, acceptRange{typ: typ, sub: sub, q: q, specificity: specificity, order: i})
	}
	slices.SortStableFunc(out, func(a, b acceptRange) int {
		if a.q != b.q {
			if a.q > b.q {
				return -1
			}
			return 1
		}
		if a.specificity != b.specificity {
			if a.specificity > b.specificity {
				return -1
			}
			return 1
		}
		return 0
	})
	return out
}

// negotiateContentType returns the index into offered (already expanded,
// lowercase MIME types) most preferred by the Accept header, or -1.
func negotiateContentType(header string, offered []string) int {
	ranges := parseAccept(header)
	for _, r := range ranges {
		for i, o := range offered {
			typ, sub, ok := strings.Cut(o, "/")
			if !ok {
				continue
			}
			if (r.typ == "*" || r.typ == typ) && (r.sub == "*" || r.sub == sub) {
				return i
			}
		}
	}
	return -1
}
