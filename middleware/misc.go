package middleware

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/TosmimForidMehtab/torge"
)

// Timeout sets a deadline on the request context. Handlers, database calls
// and outgoing requests using c.Context() observe it; an operation that fails
// with context.DeadlineExceeded is rendered as 504.
//
// Unlike http.TimeoutHandler, no second goroutine races the handler for the
// response writer: cancellation is cooperative, following normal Go context
// semantics. Handlers that ignore their context are not interrupted.
func Timeout(d time.Duration) torge.Middleware {
	return func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			ctx, cancel := context.WithTimeout(c.Context(), d)
			defer cancel()
			c.SetContext(ctx)
			return next(c)
		}
	}
}

// CSRFConfig configures cross-site request forgery protection.
type CSRFConfig struct {
	// TrustedOrigins are origins allowed to make cross-origin unsafe
	// requests, such as "https://admin.example.com".
	TrustedOrigins []string
	// BypassPatterns are net/http ServeMux patterns exempt from protection,
	// for example "POST /webhooks/" for signed webhooks.
	BypassPatterns []string
}

// CSRF rejects cross-origin state-changing requests (POST, PUT, PATCH,
// DELETE) from browsers using the Sec-Fetch-Site and Origin headers, via the
// standard library's http.CrossOriginProtection. It requires no tokens and is
// appropriate for cookie-authenticated applications. Non-browser clients,
// which send neither header, are allowed.
func CSRF(cfgs ...CSRFConfig) torge.Middleware {
	var cfg CSRFConfig
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	p := http.NewCrossOriginProtection()
	for _, o := range cfg.TrustedOrigins {
		if err := p.AddTrustedOrigin(o); err != nil {
			panic(&torge.Diagnostic{
				Code: torge.DiagInvalidConfig, What: "invalid CSRF trusted origin " + strconv.Quote(o),
				Why: err.Error(), Fix: "use scheme://host[:port] without a path",
			})
		}
	}
	for _, pattern := range cfg.BypassPatterns {
		p.AddInsecureBypassPattern(pattern)
	}
	return func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			if err := p.Check(c.Request()); err != nil {
				return torge.Forbidden("CSRF_REJECTED", "Cross-origin request rejected").Wrap(err)
			}
			return next(c)
		}
	}
}

// StaticConfig configures static file serving.
type StaticConfig struct {
	// Root is the file system to serve, for example os.DirFS("public") or an
	// embed.FS.
	Root fs.FS
	// Prefix is the URL path prefix (default "/").
	Prefix string
	// Index is served for directory requests (default "index.html").
	// Directory listings are never generated.
	Index string
	// SPA serves Index for unknown paths under Prefix, for single-page apps.
	SPA bool
	// MaxAge sets Cache-Control max-age when non-zero.
	MaxAge time.Duration
}

// Static serves files from cfg.Root for GET and HEAD requests under the
// prefix, and passes every other request to the next handler. Range and
// conditional requests are handled by http.ServeFileFS. Use it as global
// middleware (App.Use) so files are served without registering routes.
func Static(cfg StaticConfig) torge.Middleware {
	if cfg.Root == nil {
		panic(&torge.Diagnostic{Code: torge.DiagInvalidConfig, What: "StaticConfig.Root is nil",
			Why: "there is nothing to serve", Fix: "set Root, for example os.DirFS(\"public\")"})
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "/"
	}
	prefix := "/" + strings.Trim(cfg.Prefix, "/")
	if cfg.Index == "" {
		cfg.Index = "index.html"
	}
	cacheControl := ""
	if cfg.MaxAge > 0 {
		cacheControl = "public, max-age=" + strconv.Itoa(int(cfg.MaxAge.Seconds()))
	}
	return func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			r := c.Request()
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				return next(c)
			}
			name, ok := staticName(r.URL.Path, prefix)
			if !ok {
				return next(c)
			}
			file, found := resolveFile(cfg.Root, name, cfg.Index)
			if !found && cfg.SPA && path.Ext(name) == "" {
				file, found = resolveFile(cfg.Root, cfg.Index, cfg.Index)
			}
			if !found {
				return next(c)
			}
			if cacheControl != "" {
				c.Header("Cache-Control", cacheControl)
			}
			serveFS(c, cfg.Root, file)
			return nil
		}
	}
}

// staticName maps a URL path under prefix to a file system name.
func staticName(urlPath, prefix string) (string, bool) {
	if prefix != "/" {
		if urlPath != prefix && !strings.HasPrefix(urlPath, prefix+"/") {
			return "", false
		}
		urlPath = strings.TrimPrefix(urlPath, prefix)
	}
	name := strings.TrimPrefix(path.Clean("/"+urlPath), "/")
	if name == "" {
		name = "."
	}
	return name, fs.ValidPath(name)
}

func resolveFile(fsys fs.FS, name, index string) (string, bool) {
	info, err := fs.Stat(fsys, name)
	if err != nil {
		return "", false
	}
	if info.IsDir() {
		idx := path.Join(name, index)
		if info, err := fs.Stat(fsys, idx); err == nil && !info.IsDir() {
			return idx, true
		}
		return "", false
	}
	return name, true
}

// serveFS serves name without http.ServeFileFS's index.html redirect.
func serveFS(c *torge.Context, fsys fs.FS, name string) {
	f, err := fsys.Open(name)
	if err != nil {
		c.HandleError(torge.NotFound(torge.CodeNotFound, "Not found"))
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		c.HandleError(err)
		return
	}
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(c.Response(), c.Request(), info.Name(), info.ModTime(), rs)
		return
	}
	http.ServeFileFS(c.Response(), c.Request(), fsys, name)
}
