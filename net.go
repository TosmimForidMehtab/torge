package torge

import (
	"net"
	"net/netip"
	"slices"
	"strings"
)

// remoteAddr parses the connection's peer address.
func (c *Context) remoteAddr() (netip.Addr, bool) {
	host := c.req.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func (a *App) trusted(addr netip.Addr) bool {
	for _, p := range a.proxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// IsFromTrustedProxy reports whether the connection comes from a configured
// trusted proxy (see WithTrustedProxies).
func (c *Context) IsFromTrustedProxy() bool {
	if c.app == nil || len(c.app.proxies) == 0 {
		return false
	}
	addr, ok := c.remoteAddr()
	return ok && c.app.trusted(addr)
}

// RealIP returns the client IP address. X-Forwarded-For is honored only when
// the connection comes from a trusted proxy; the header is then walked from
// right to left and the first address that is not itself a trusted proxy is
// returned. Without trusted proxies, the peer address is returned, so clients
// cannot spoof their IP.
func (c *Context) RealIP() string {
	if c.realIP != "" {
		return c.realIP
	}
	if c.app == nil || len(c.app.proxies) == 0 {
		// No proxy is trusted, so the peer address is the answer: slice the
		// host out without parsing or allocating.
		host := c.req.RemoteAddr
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		c.realIP = host
		return host
	}
	addr, ok := c.remoteAddr()
	if !ok {
		c.realIP = c.req.RemoteAddr
		return c.realIP
	}
	ip := addr.String()
	if c.app != nil && c.app.trusted(addr) {
		if fwd := forwardedClient(c.req.Header.Values("X-Forwarded-For"), c.app); fwd != "" {
			ip = fwd
		}
	}
	c.realIP = ip
	return ip
}

func forwardedClient(values []string, a *App) string {
	var hops []string
	for _, v := range values {
		for part := range strings.SplitSeq(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				hops = append(hops, part)
			}
		}
	}
	for i, hop := range slices.Backward(hops) {
		addr, err := netip.ParseAddr(hop)
		if err != nil {
			return ""
		}
		addr = addr.Unmap()
		if !a.trusted(addr) || i == 0 {
			return addr.String()
		}
	}
	return ""
}

// Scheme returns "https" or "http". X-Forwarded-Proto is honored only from
// trusted proxies.
func (c *Context) Scheme() string {
	if c.req.TLS != nil {
		return "https"
	}
	if c.IsFromTrustedProxy() {
		if p := strings.ToLower(c.req.Header.Get("X-Forwarded-Proto")); p == "https" || p == "http" {
			return p
		}
	}
	return "http"
}
