package torge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"

	"github.com/TosmimForidMehtab/torge/internal/binding"
	"github.com/TosmimForidMehtab/torge/validate"
)

// Strict is an embedded marker enabling strict binding for an input type.
// Strict inputs reject JSON bodies with unknown fields and fail requests
// carrying undeclared query parameters with a 422 listing them:
//
//	type ListUsers struct {
//		torge.Strict
//		Q string `query:"q" validate:"max=100"`
//	}
//
// It applies to Context.Bind, Context.BindJSON and typed handlers alike.
// Slice fields additionally accept `query:"tag,comma"` to split single
// values on commas, and time.Time fields accept `layout:"2006-01-02"` to
// parse non-RFC3339 timestamps.
type Strict = binding.Strict

// Bind populates dst (a pointer to a struct) from the request and validates
// it:
//
//   - fields tagged `path`, `query`, `header` or `cookie` are bound from the
//     corresponding request parameters (with optional `default` tags);
//   - the request body is decoded into a field named Body if present,
//     otherwise into dst itself;
//   - `validate` tags and Validatable are then checked.
//
// Parameters are bound after the body, so a body can never overwrite them.
// Failures are returned as *Error values (400, 413, 415 or 422) ready to be
// returned from the handler.
func (c *Context) Bind(dst any) error {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("torge: Bind requires a non-nil pointer to a struct, got %T", dst)
	}
	plan, err := binding.PlanFor(rv.Elem().Type())
	if err != nil {
		return err
	}
	if err := c.bindPlan(rv.Elem(), plan); err != nil {
		return err
	}
	return c.Validate(dst)
}

// BindJSON decodes the request body into dst and validates it. An empty body
// is rejected. When dst is a struct embedding Strict, unknown JSON fields
// are rejected.
func (c *Context) BindJSON(dst any) error {
	present, err := c.decodeBody(dst, c.strictBody(dst))
	if err != nil {
		return err
	}
	if !present {
		return BadRequest(CodeBadRequest, "Request body is required")
	}
	return c.Validate(dst)
}

// BindFormValues binds values (form fields, for example the url.Values
// returned by upload.Stream alongside streamed files) into dst's
// query-tagged fields and validates the result with the application's
// validator. Path, header and cookie parameters are left at their zero
// values. Unknown values fail only when dst embeds Strict.
func (c *Context) BindFormValues(dst any, values url.Values) error {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("torge: BindFormValues requires a non-nil pointer to a struct, got %T", dst)
	}
	plan, err := binding.PlanFor(rv.Elem().Type())
	if err != nil {
		return err
	}
	if err := plan.Bind(rv.Elem(), queryValuesSource{values}); err != nil {
		return AsError(err)
	}
	return c.Validate(dst)
}

// queryValuesSource binds url.Values as the query location.
type queryValuesSource struct{ values url.Values }

func (s queryValuesSource) PathValue(string) (string, bool) { return "", false }
func (s queryValuesSource) QueryValues() url.Values         { return s.values }
func (s queryValuesSource) HeaderValues(string) []string    { return nil }
func (s queryValuesSource) CookieValue(string) (string, bool) {
	return "", false
}

// strictBodyCache remembers per input type whether strict bodies apply, so
// BindJSON pays no reflection or plan lookup on the hot path after the
// first call for a type.
var strictBodyCache sync.Map // reflect.Type -> bool

// strictBody reports whether dst's binding plan requires strict bodies. It
// stays off the hot path: custom serializers ignore strictness, and the
// answer is cached per type.
func (c *Context) strictBody(dst any) bool {
	if _, ok := c.serializer().(JSONSerializer); !ok {
		return false
	}
	t := reflect.TypeOf(dst)
	if t == nil || t.Kind() != reflect.Pointer || t.Elem().Kind() != reflect.Struct {
		return false
	}
	if v, ok := strictBodyCache.Load(t); ok {
		return v.(bool)
	}
	plan, err := binding.PlanFor(t.Elem())
	strict := err == nil && plan.StrictBody
	strictBodyCache.Store(t, strict)
	return strict
}

// Validate validates v with the application's validator, converting failures
// into a 422 *Error with field details.
func (c *Context) Validate(v any) error {
	var val Validator = validate.Default
	if c.app != nil && c.app.opts.Validator != nil {
		val = c.app.opts.Validator
	}
	err := val.Validate(v)
	if err == nil {
		return nil
	}
	var verrs validate.Errors
	if errors.As(err, &verrs) {
		return AsError(verrs)
	}
	return err
}

func (c *Context) bindPlan(dst reflect.Value, plan *binding.Plan) error {
	if plan.BodyType != nil {
		var target any
		if plan.WholeBody {
			target = dst.Addr().Interface()
		} else {
			target = dst.FieldByIndex(plan.BodyIndex).Addr().Interface()
		}
		if _, err := c.decodeBody(target, plan.StrictBody); err != nil {
			return err
		}
	}
	// The strict query check runs even when the input declares no
	// parameters: that is exactly when every query parameter is undeclared.
	if len(plan.Params) > 0 || plan.StrictQuery {
		if err := plan.Bind(dst, contextSource{c}); err != nil {
			return AsError(err)
		}
	}
	return nil
}

// decodeBody decodes the body into dst. It reports whether a body was present.
// Strict bodies reject unknown JSON fields; strictness is a no-op for custom
// serializers.
func (c *Context) decodeBody(dst any, strict bool) (bool, error) {
	r := c.req
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return false, nil
	}
	s := c.serializer()
	if strict {
		if js, ok := s.(JSONSerializer); ok {
			js.DisallowUnknownFields = true
			s = js
		}
	}
	if ct := r.Header.Get("Content-Type"); ct != "" && !contentTypeMatches(ct, s.ContentType()) {
		return false, NewError(http.StatusUnsupportedMediaType, CodeUnsupportedMedia,
			fmt.Sprintf("Content-Type %q is not supported; send %s", ct, baseMediaType(s.ContentType())))
	}
	var err error
	if js, ok := s.(JSONSerializer); ok {
		err = js.decode(r.Body, r.ContentLength, dst)
	} else {
		err = s.Decode(r.Body, dst)
	}
	if err == nil {
		return true, nil
	}
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	return true, decodeError(err)
}

func decodeError(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return AsError(err)
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		field := typeErr.Field
		if field == "" {
			field = "body"
		}
		return AsError(validate.Errors{{
			Field:   field,
			Rule:    "type",
			Message: "must be " + jsonTypeName(typeErr.Type),
		}})
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, errTrailingData) {
		return BadRequest(CodeInvalidJSON, "Request body is not valid JSON").Wrap(err)
	}
	if after, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		return BadRequest(CodeInvalidJSON, "Request body contains an unknown field").
			WithDetails(map[string]string{"field": strings.Trim(after, `"`)}).
			Wrap(err)
	}
	return BadRequest(CodeInvalidJSON, "Request body could not be decoded").Wrap(err)
}

func jsonTypeName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Map, reflect.Struct:
		return "an object"
	default:
		return "a " + t.String()
	}
}

// baseMediaType returns the media type of a Content-Type value without its
// parameters, as mime.ParseMediaType does (the result may differ in case; it
// is only compared case-insensitively), or ct itself if it does not parse.
func baseMediaType(ct string) string {
	if mt, ok := simpleMediaType(ct); ok {
		return mt
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return ct
	}
	return mt
}

// simpleMediaType handles the common shapes "type/subtype" and
// "type/subtype; charset=utf-8" without allocating. It reports false for
// anything else, which then goes through mime.ParseMediaType.
func simpleMediaType(ct string) (string, bool) {
	mt, params, hasParams := strings.Cut(ct, ";")
	mt = strings.TrimSpace(mt)
	if hasParams && !strings.EqualFold(strings.TrimSpace(params), "charset=utf-8") {
		return "", false
	}
	slash := -1
	for i := 0; i < len(mt); i++ {
		switch c := mt[i]; {
		case c == '/':
			if slash >= 0 {
				return "", false
			}
			slash = i
		case !isTokenByte(c):
			return "", false
		}
	}
	if slash <= 0 || slash == len(mt)-1 {
		return "", false
	}
	return mt, true
}

// tokenBytes marks the bytes allowed in an RFC 7230 token.
var tokenBytes = func() (t [256]bool) {
	for c := '!'; c <= '~'; c++ {
		t[c] = !strings.ContainsRune(`()<>@,;:\"/[]?={}`, c)
	}
	return t
}()

func isTokenByte(c byte) bool { return tokenBytes[c] }

// contentTypeMatches reports whether a request content type is acceptable for
// the serializer, treating any "+json" suffix type as JSON.
func contentTypeMatches(got, want string) bool {
	g, w := baseMediaType(got), baseMediaType(want)
	if strings.EqualFold(g, w) {
		return true
	}
	return strings.EqualFold(w, "application/json") && len(g) >= 5 && strings.EqualFold(g[len(g)-5:], "+json")
}

// contextSource adapts a Context to binding.Source.
type contextSource struct{ c *Context }

func (s contextSource) PathValue(name string) (string, bool) {
	c := s.c
	if c.rt == nil {
		return "", false
	}
	for i, n := range c.rt.paramNames {
		if n == name {
			return c.vals[i], true
		}
	}
	return "", false
}

func (s contextSource) QueryValues() url.Values { return s.c.QueryValues() }

func (s contextSource) HeaderValues(name string) []string { return s.c.req.Header[name] }

func (s contextSource) CookieValue(name string) (string, bool) {
	ck, err := s.c.req.Cookie(name)
	if err != nil {
		return "", false
	}
	return ck.Value, true
}
