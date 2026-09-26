package torge

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/TosmimForidMehtab/torge/openapi"
)

// route is a registered route. Options are resolved when the application
// starts, so group middleware and options apply regardless of whether they
// were added before or after the route was registered.
type route struct {
	method     string
	path       string
	handler    Handler
	handlerFn  string
	group      *Group
	opts       []RouteOption
	paramNames []string
	module     string
	location   string

	// Resolved at start.
	cfg      *routeConfig
	composed Handler
}

// routeConfig is the resolved configuration of a route.
type routeConfig struct {
	name       string
	middleware []Middleware
	bodyLimit  int64
	doc        operationDoc
}

// operationDoc holds OpenAPI metadata for a route.
type operationDoc struct {
	hidden          bool
	summary         string
	description     string
	operationID     string
	tags            []string
	deprecated      bool
	security        *[]openapi.SecurityRequirement
	successStatus   int
	input           reflect.Type
	body            reflect.Type
	bodyContentType string
	bodyExample     any
	hasBodyExample  bool
	responses       map[int]responseDoc
}

type responseDoc struct {
	description string
	typ         reflect.Type
	example     any
	hasExample  bool
}

// RouteOption configures a route or, when passed to Group, every route of the
// group. A Middleware is also a RouteOption.
type RouteOption interface {
	applyRoute(*routeConfig)
}

type routeOptionFunc func(*routeConfig)

func (f routeOptionFunc) applyRoute(r *routeConfig) { f(r) }

// Name names a route so that its URL can be built with App.URL.
func Name(name string) RouteOption {
	return routeOptionFunc(func(r *routeConfig) { r.name = name })
}

// BodyLimit overrides the application's request body limit for a route. Use a
// negative value to disable the limit (for example for streaming uploads that
// enforce their own limits).
func BodyLimit(bytes int64) RouteOption {
	return routeOptionFunc(func(r *routeConfig) { r.bodyLimit = bytes })
}

// Summary sets the OpenAPI summary.
func Summary(s string) RouteOption {
	return routeOptionFunc(func(r *routeConfig) { r.doc.summary = s })
}

// Description sets the OpenAPI description.
func Description(s string) RouteOption {
	return routeOptionFunc(func(r *routeConfig) { r.doc.description = s })
}

// OperationID sets the OpenAPI operation ID. By default the route name is
// used, or one is derived from the method and path.
func OperationID(id string) RouteOption {
	return routeOptionFunc(func(r *routeConfig) { r.doc.operationID = id })
}

// Tags adds OpenAPI tags.
func Tags(tags ...string) RouteOption {
	return routeOptionFunc(func(r *routeConfig) { r.doc.tags = append(r.doc.tags, tags...) })
}

// Deprecated marks the operation as deprecated in OpenAPI.
func Deprecated() RouteOption {
	return routeOptionFunc(func(r *routeConfig) { r.doc.deprecated = true })
}

// Sunset marks the route as deprecated and announces its retirement with
// RFC 8594 Deprecation/Sunset response headers. date is the retirement date
// in IMF-fixdate form ("Mon, 02 Jan 2006 15:04:05 GMT"); an empty date sends
// only the Deprecation header. The date is validated at registration, so a
// typo fails fast instead of shipping a bogus header. It applies to groups
// as well, so a whole version can sunset at once:
//
//	legacy := app.Version("v0", torge.Sunset("Mon, 01 Jun 2026 00:00:00 GMT"))
func Sunset(date string) RouteOption {
	if date != "" {
		if _, err := http.ParseTime(date); err != nil {
			panic(&Diagnostic{
				Code: DiagInvalidConfig, What: "invalid Sunset date " + strconv.Quote(date),
				Why: err.Error(), Fix: `use IMF-fixdate, e.g. "Mon, 01 Jun 2026 00:00:00 GMT"`,
			})
		}
	}
	return routeOptionFunc(func(r *routeConfig) {
		r.doc.deprecated = true
		r.middleware = append(r.middleware, Middleware(func(next Handler) Handler {
			return func(c *Context) error {
				c.Header("Deprecation", "true")
				if date != "" {
					c.Header("Sunset", date)
				}
				return next(c)
			}
		}))
	})
}

// Hidden excludes the route from the OpenAPI document.
func Hidden() RouteOption {
	return routeOptionFunc(func(r *routeConfig) { r.doc.hidden = true })
}

// Security declares the security schemes (by name, as registered with
// App.OpenAPI) that protect the route. Documentation only: enforcement is done
// by authentication middleware.
func Security(schemes ...string) RouteOption {
	return routeOptionFunc(func(r *routeConfig) {
		reqs := make([]openapi.SecurityRequirement, 0, len(schemes))
		for _, s := range schemes {
			reqs = append(reqs, openapi.SecurityRequirement{s: {}})
		}
		r.doc.security = &reqs
	})
}

// Public documents that the route requires no authentication, overriding
// security declared on a group or globally.
func Public() RouteOption {
	return routeOptionFunc(func(r *routeConfig) {
		empty := []openapi.SecurityRequirement{}
		r.doc.security = &empty
	})
}

// Status sets the success status code written by typed handlers and
// documented in OpenAPI. The default is 200 (204 when the output is nil).
func Status(code int) RouteOption {
	return routeOptionFunc(func(r *routeConfig) { r.doc.successStatus = code })
}

// Body documents the request body type of an untyped handler.
func Body[T any]() RouteOption {
	t := reflect.TypeFor[T]()
	return routeOptionFunc(func(r *routeConfig) { r.doc.body = t })
}

// Returns documents a response body type for a status code.
func Returns[T any](status int, description string) RouteOption {
	t := reflect.TypeFor[T]()
	return routeOptionFunc(func(r *routeConfig) {
		if r.doc.responses == nil {
			r.doc.responses = make(map[int]responseDoc)
		}
		r.doc.responses[status] = responseDoc{description: description, typ: t}
	})
}

// Responds documents a response without a body, such as an error status.
func Responds(status int, description string) RouteOption {
	return routeOptionFunc(func(r *routeConfig) {
		if r.doc.responses == nil {
			r.doc.responses = make(map[int]responseDoc)
		}
		r.doc.responses[status] = responseDoc{description: description}
	})
}

// RequestContentType overrides the documented request body media type
// (default application/json). Use it with Body for uploads:
//
//	app.POST("/avatar", uploadAvatar,
//		torge.Body[AvatarForm](),
//		torge.RequestContentType("multipart/form-data"))
//
// File parts are declared with format:"binary" fields, for example a File
// string field tagged json:"file" format:"binary".
func RequestContentType(contentType string) RouteOption {
	return routeOptionFunc(func(r *routeConfig) { r.doc.bodyContentType = contentType })
}

// RequestExample documents an example request body.
func RequestExample(example any) RouteOption {
	return routeOptionFunc(func(r *routeConfig) { r.doc.bodyExample, r.doc.hasBodyExample = example, true })
}

// ResponseExample documents an example response body for a status code. It
// pairs with Returns and Responds:
//
//	torge.Post(api, "/users", svc.Create,
//		torge.Status(201),
//		torge.Returns[User](201, "Created"),
//		torge.ResponseExample(201, User{ID: "1", Name: "Ada"}))
func ResponseExample(status int, example any) RouteOption {
	return routeOptionFunc(func(r *routeConfig) {
		if r.doc.responses == nil {
			r.doc.responses = make(map[int]responseDoc)
		}
		d := r.doc.responses[status]
		d.example, d.hasExample = example, true
		r.doc.responses[status] = d
	})
}

// Scopes documents the OAuth2 scopes required by the route, merging into
// security declared with Security on the route or its groups:
//
//	api := app.Group("/v1", torge.Security("oauth"))
//	api.GET("/me", getMe, torge.Scopes("oauth", "profile:read"))
func Scopes(scheme string, scopes ...string) RouteOption {
	return routeOptionFunc(func(r *routeConfig) {
		if r.doc.security == nil {
			reqs := []openapi.SecurityRequirement{{scheme: scopes}}
			r.doc.security = &reqs
			return
		}
		for i, req := range *r.doc.security {
			if _, ok := req[scheme]; ok {
				(*r.doc.security)[i][scheme] = scopes
				return
			}
		}
		*r.doc.security = append(*r.doc.security, openapi.SecurityRequirement{scheme: scopes})
	})
}

// RouteInfo describes a registered route for introspection and tooling.
type RouteInfo struct {
	Method     string   `json:"method"`
	Path       string   `json:"path"`
	Name       string   `json:"name,omitempty"`
	Handler    string   `json:"handler"`
	Middleware []string `json:"middleware,omitempty"`
	Module     string   `json:"module,omitempty"`
	Location   string   `json:"location,omitempty"`
	Deprecated bool     `json:"deprecated,omitempty"`
}

// resolve applies group options (outermost group first) and then the route's
// own options, returning the effective configuration.
func (r *route) resolve() *routeConfig {
	cfg := &routeConfig{bodyLimit: 0}
	var groups []*Group
	for g := r.group; g != nil && g.parent != nil; g = g.parent {
		groups = append(groups, g)
	}
	for _, g := range slices.Backward(groups) {

		for _, o := range g.opts {
			o.applyRoute(cfg)
		}
		cfg.middleware = append(cfg.middleware, g.mws...)
	}
	for _, o := range r.opts {
		o.applyRoute(cfg)
	}
	return cfg
}

func (r *route) info() RouteInfo {
	cfg := r.cfg
	if cfg == nil {
		cfg = r.resolve()
	}
	info := RouteInfo{
		Method:     r.method,
		Path:       r.path,
		Name:       cfg.name,
		Handler:    firstNonEmpty(r.handlerFn, funcName(r.handler)),
		Module:     r.module,
		Location:   r.location,
		Deprecated: cfg.doc.deprecated,
	}
	for _, m := range cfg.middleware {
		info.Middleware = append(info.Middleware, funcName(m))
	}
	return info
}

// buildURL substitutes parameters into the route pattern.
func (r *route) buildURL(pairs []string) (string, error) {
	if len(pairs)%2 != 0 {
		return "", fmt.Errorf("torge: URL parameters must be name/value pairs")
	}
	values := make(map[string]string, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		values[pairs[i]] = pairs[i+1]
	}
	if r.path == "/" {
		return "/", nil
	}
	segs := strings.Split(r.path[1:], "/")
	for i, seg := range segs {
		if seg == "" || (seg[0] != ':' && seg[0] != '*') {
			continue
		}
		name := seg[1:]
		if name == "" {
			name = "*"
		}
		v, ok := values[name]
		if !ok {
			return "", fmt.Errorf("torge: missing URL parameter %q for route %s", name, r.path)
		}
		if seg[0] == '*' {
			parts := strings.Split(v, "/")
			for j, p := range parts {
				parts[j] = url.PathEscape(p)
			}
			segs[i] = strings.Join(parts, "/")
		} else {
			segs[i] = url.PathEscape(v)
		}
	}
	return "/" + strings.Join(segs, "/"), nil
}

// standardMethods are the methods registered by Any and documented by OpenAPI.
var standardMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodOptions,
}
