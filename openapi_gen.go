package torge

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/TosmimForidMehtab/torge/internal/binding"
	"github.com/TosmimForidMehtab/torge/openapi"
	"github.com/TosmimForidMehtab/torge/validate"
)

// OpenAPIConfig enables and configures the generated OpenAPI document.
type OpenAPIConfig struct {
	Info    openapi.Info
	Servers []openapi.Server
	Tags    []openapi.Tag
	// SecuritySchemes are the available authentication schemes, referenced
	// by name from Security route options.
	SecuritySchemes map[string]*openapi.SecurityScheme
	// Security is the default requirement for all operations.
	Security []openapi.SecurityRequirement
	// SpecPath serves the JSON document (default /openapi.json, "-"
	// disables).
	SpecPath string
	// DocsPath serves interactive documentation (default /docs, "-"
	// disables).
	DocsPath string
	// UI selects the documentation renderer (default Swagger UI).
	UI openapi.UI
	// RouteOptions apply to the spec and docs routes, for example
	// authentication middleware to keep documentation private.
	RouteOptions []RouteOption
}

type openAPIState struct {
	enabled bool
	cfg     OpenAPIConfig
	schemes map[string]*openapi.SecurityScheme
	tags    []openapi.Tag
	doc     *openapi.Document
	json    []byte
}

func (a *App) openAPIStateLocked() *openAPIState {
	if a.openapi == nil {
		a.openapi = &openAPIState{schemes: make(map[string]*openapi.SecurityScheme)}
	}
	return a.openapi
}

// OpenAPI enables the OpenAPI document and documentation UI. The document is
// generated at startup from route definitions: typed handlers contribute
// parameter, body and response schemas (including validation constraints),
// and route options contribute summaries, tags and security.
func (a *App) OpenAPI(cfg OpenAPIConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mustBeRegistering(callerLocation(), "OpenAPI configuration")
	setDefault(&cfg.SpecPath, "/openapi.json")
	setDefault(&cfg.DocsPath, "/docs")
	setDefault(&cfg.UI, openapi.SwaggerUI)
	setDefault(&cfg.Info.Title, "API")
	setDefault(&cfg.Info.Version, "1.0.0")
	st := a.openAPIStateLocked()
	st.enabled, st.cfg = true, cfg
	maps.Copy(st.schemes, cfg.SecuritySchemes)
	st.tags = append(st.tags, cfg.Tags...)
}

// AddSecurityScheme registers an OpenAPI security scheme, for example from a
// module or an authentication extension.
func (a *App) AddSecurityScheme(name string, s *openapi.SecurityScheme) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.openAPIStateLocked().schemes[name] = s
}

// AddTag registers an OpenAPI tag description.
func (a *App) AddTag(tag openapi.Tag) {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.openAPIStateLocked()
	st.tags = append(st.tags, tag)
}

// OpenAPIDocument returns the generated document. It is available after
// Start when OpenAPI is enabled.
func (a *App) OpenAPIDocument() (*openapi.Document, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.openapi == nil || !a.openapi.enabled {
		return nil, fmt.Errorf("torge: OpenAPI is not enabled; call app.OpenAPI")
	}
	if a.openapi.doc == nil {
		return nil, fmt.Errorf("torge: the OpenAPI document is generated at Start")
	}
	return a.openapi.doc, nil
}

func (a *App) registerOpenAPIRoutes() {
	a.mu.Lock()
	st := a.openapi
	a.mu.Unlock()
	if st == nil || !st.enabled {
		return
	}
	cfg := st.cfg
	opts := append([]RouteOption{Hidden()}, cfg.RouteOptions...)
	if cfg.SpecPath != "-" {
		a.GET(cfg.SpecPath, func(c *Context) error {
			c.Header("Cache-Control", "no-cache")
			return c.Bytes(http.StatusOK, "application/json; charset=utf-8", st.json)
		}, opts...)
	}
	if cfg.DocsPath != "-" && cfg.SpecPath != "-" {
		page, err := openapi.DocsHTML(cfg.UI, cfg.Info.Title, cfg.SpecPath)
		if err != nil {
			a.mu.Lock()
			a.addDiagnostic(&Diagnostic{Code: DiagInvalidConfig, What: "cannot render docs page: " + err.Error()})
			a.mu.Unlock()
			return
		}
		a.GET(cfg.DocsPath, func(c *Context) error {
			// The docs page loads its renderer from a CDN.
			c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; img-src 'self' data: https:; font-src 'self' data: https:; connect-src 'self'")
			return c.Bytes(http.StatusOK, "text/html; charset=utf-8", page)
		}, opts...)
	}
}

// buildOpenAPI generates the document from frozen routes. Callers hold a.mu.
func (a *App) buildOpenAPI() {
	st := a.openapi
	if st == nil || !st.enabled {
		return
	}
	gen := openapi.NewGenerator()
	cfg := st.cfg
	doc := &openapi.Document{
		OpenAPI:  openapi.Version,
		Info:     cfg.Info,
		Servers:  cfg.Servers,
		Paths:    make(map[string]openapi.PathItem),
		Security: cfg.Security,
		Tags:     st.tags,
	}
	errRef := gen.Schema(reflect.TypeFor[ErrorBody]())
	usedIDs := make(map[string]int)
	for _, r := range a.routes {
		rc := r.cfg
		if rc.doc.hidden || r.method == http.MethodHead || r.method == http.MethodOptions && rc.doc.input == nil || !isDocumentedMethod(r.method) {
			continue
		}
		path, pathParams := openAPIPath(r.path)
		op := &openapi.Operation{
			Summary:     rc.doc.summary,
			Description: rc.doc.description,
			Tags:        rc.doc.tags,
			Deprecated:  rc.doc.deprecated,
			Security:    rc.doc.security,
			Responses:   make(map[string]*openapi.Response),
		}
		op.OperationID = uniqueID(usedIDs, firstNonEmpty(rc.doc.operationID, rc.name, deriveOperationID(r.method, r.path)))

		documented := make(map[string]bool)
		var bodyType reflect.Type
		var bodySchema *openapi.Schema
		if in := rc.doc.input; in != nil && in.Kind() == reflect.Struct {
			if plan, err := binding.PlanFor(in); err == nil {
				for _, p := range plan.Params {
					op.Parameters = append(op.Parameters, paramDoc(gen, p))
					if p.In == binding.Path {
						documented[p.Name] = true
					}
				}
				switch {
				case plan.BodyIndex != nil:
					bodyType = plan.BodyType
					bodySchema = gen.Schema(bodyType)
				case plan.WholeBody && hasBody(r.method):
					bodyType = plan.BodyType
					if len(plan.Params) > 0 {
						bodySchema = gen.InlineSchema(in, binding.IsParamField)
					} else {
						bodySchema = gen.Schema(in)
					}
				}
			}
		}
		if bodySchema == nil && rc.doc.body != nil {
			bodyType = rc.doc.body
			bodySchema = gen.Schema(bodyType)
		}
		for _, name := range pathParams {
			if !documented[name] {
				op.Parameters = append(op.Parameters, &openapi.Parameter{
					Name: docParamName(name), In: "path", Required: true, Schema: &openapi.Schema{Type: "string"},
				})
			}
		}
		if bodySchema != nil {
			op.RequestBody = &openapi.RequestBody{
				Required: bodyType.Kind() != reflect.Pointer,
				Content:  map[string]*openapi.MediaType{"application/json": {Schema: bodySchema}},
			}
		}

		success := rc.doc.successStatus
		if success == 0 {
			success = http.StatusOK
		}
		if out, ok := rc.doc.responses[0]; ok {
			op.Responses[strconv.Itoa(success)] = &openapi.Response{
				Description: http.StatusText(success),
				Content:     map[string]*openapi.MediaType{"application/json": {Schema: gen.Schema(out.typ)}},
			}
		} else if rc.doc.input != nil {
			op.Responses["204"] = &openapi.Response{Description: http.StatusText(http.StatusNoContent)}
		}
		for status, resp := range rc.doc.responses {
			if status == 0 {
				continue
			}
			desc := firstNonEmpty(resp.description, http.StatusText(status))
			or := &openapi.Response{Description: desc}
			if resp.typ != nil {
				or.Content = map[string]*openapi.MediaType{"application/json": {Schema: gen.Schema(resp.typ)}}
			} else if status >= 400 {
				or.Content = map[string]*openapi.MediaType{"application/json": {Schema: errRef}}
			}
			op.Responses[strconv.Itoa(status)] = or
		}
		if len(op.Responses) == 0 {
			op.Responses[strconv.Itoa(success)] = &openapi.Response{Description: http.StatusText(success)}
		}
		if len(op.Parameters) > 0 || op.RequestBody != nil {
			if _, ok := op.Responses["422"]; !ok {
				op.Responses["422"] = &openapi.Response{
					Description: "Validation failed",
					Content:     map[string]*openapi.MediaType{"application/json": {Schema: errRef}},
				}
			}
		}
		op.Responses["default"] = &openapi.Response{
			Description: "Error",
			Content:     map[string]*openapi.MediaType{"application/json": {Schema: errRef}},
		}

		item := doc.Paths[path]
		if item == nil {
			item = make(openapi.PathItem)
			doc.Paths[path] = item
		}
		item[strings.ToLower(r.method)] = op
	}
	doc.Components = &openapi.Components{Schemas: gen.Schemas()}
	if len(st.schemes) > 0 {
		doc.Components.SecuritySchemes = st.schemes
	}
	sort.Slice(doc.Tags, func(i, j int) bool { return doc.Tags[i].Name < doc.Tags[j].Name })
	st.doc = doc
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		a.addDiagnostic(&Diagnostic{Code: DiagInvalidConfig, What: "cannot encode OpenAPI document: " + err.Error(),
			Why: "a schema example or default is not JSON-encodable", Fix: "check example and default tags"})
		return
	}
	st.json = b
}

func paramDoc(gen *openapi.Generator, p binding.Param) *openapi.Parameter {
	s := gen.Schema(p.Field.Type)
	openapi.FieldDoc(s, p.Field)
	rules, _ := validate.ParseTag(p.Field.Tag.Get("validate"))
	required := p.In == binding.Path
	base := p.Field.Type
	for base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	for _, r := range rules {
		if r.Name == "required" {
			required = true
			continue
		}
		if r.Name == "dive" {
			break
		}
		openapi.ApplyRule(s, base, r)
	}
	if pattern, ok := p.Field.Tag.Lookup("pattern"); ok {
		s.Pattern = pattern
	}
	desc := s.Description
	s.Description = ""
	return &openapi.Parameter{Name: p.Name, In: string(p.In), Required: required, Description: desc, Schema: s}
}

func hasBody(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch
}

func isDocumentedMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
		http.MethodOptions, http.MethodTrace:
		return true
	}
	return false
}

// openAPIPath converts "/users/:id/*rest" to "/users/{id}/{rest}".
func openAPIPath(p string) (string, []string) {
	if p == "/" {
		return p, nil
	}
	segs := strings.Split(p[1:], "/")
	var names []string
	for i, s := range segs {
		if s != "" && (s[0] == ':' || s[0] == '*') {
			name := s[1:]
			if name == "" {
				name = "*"
			}
			names = append(names, name)
			segs[i] = "{" + docParamName(name) + "}"
		}
	}
	return "/" + strings.Join(segs, "/"), names
}

func docParamName(name string) string {
	if name == "*" {
		return "wildcard"
	}
	return name
}

func deriveOperationID(method, path string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(method))
	for seg := range strings.SplitSeq(path, "/") {
		seg = strings.TrimLeft(seg, ":*")
		if seg == "" {
			continue
		}
		b.WriteByte('_')
		for _, r := range seg {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
				b.WriteRune(r)
			} else {
				b.WriteByte('_')
			}
		}
	}
	return b.String()
}

func uniqueID(used map[string]int, id string) string {
	used[id]++
	if n := used[id]; n > 1 {
		return id + "_" + strconv.Itoa(n)
	}
	return id
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
