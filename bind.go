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

	"github.com/TosmimForidMehtab/torge/internal/binding"
	"github.com/TosmimForidMehtab/torge/validate"
)

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
// is rejected.
func (c *Context) BindJSON(dst any) error {
	present, err := c.decodeBody(dst)
	if err != nil {
		return err
	}
	if !present {
		return BadRequest(CodeBadRequest, "Request body is required")
	}
	return c.Validate(dst)
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
		if _, err := c.decodeBody(target); err != nil {
			return err
		}
	}
	if len(plan.Params) > 0 {
		if err := plan.Bind(dst, contextSource{c}); err != nil {
			return AsError(err)
		}
	}
	return nil
}

// decodeBody decodes the body into dst. It reports whether a body was present.
func (c *Context) decodeBody(dst any) (bool, error) {
	r := c.req
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return false, nil
	}
	s := c.serializer()
	if ct := r.Header.Get("Content-Type"); ct != "" && !contentTypeMatches(ct, s.ContentType()) {
		return false, NewError(http.StatusUnsupportedMediaType, CodeUnsupportedMedia,
			fmt.Sprintf("Content-Type %q is not supported; send %s", ct, baseMediaType(s.ContentType())))
	}
	err := s.Decode(r.Body, dst)
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
	if strings.HasPrefix(err.Error(), "json: unknown field ") {
		return BadRequest(CodeInvalidJSON, "Request body contains an unknown field").
			WithDetails(map[string]string{"field": strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)}).
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

func baseMediaType(ct string) string {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return ct
	}
	return mt
}

// contentTypeMatches reports whether a request content type is acceptable for
// the serializer, treating any "+json" suffix type as JSON.
func contentTypeMatches(got, want string) bool {
	g, w := baseMediaType(got), baseMediaType(want)
	if strings.EqualFold(g, w) {
		return true
	}
	return w == "application/json" && strings.HasSuffix(strings.ToLower(g), "+json")
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
