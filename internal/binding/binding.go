// Package binding compiles per-type plans that bind request parameters (path,
// query, header, cookie) into struct fields. Plans are computed once per type
// and cached; binding a request only walks the precomputed parameter list.
package binding

import (
	"encoding"
	"fmt"
	"net/textproto"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/TosmimForidMehtab/torge/validate"
)

// Location is where a parameter is read from.
type Location string

// Parameter locations.
const (
	Path   Location = "path"
	Query  Location = "query"
	Header Location = "header"
	Cookie Location = "cookie"
)

var locations = [...]Location{Path, Query, Header, Cookie}

// Param describes one bound struct field.
type Param struct {
	Name       string
	In         Location
	Index      []int
	Field      reflect.StructField
	Default    string
	HasDefault bool
	multi      bool
	decode     decoder
}

// Plan describes how to bind a struct type.
type Plan struct {
	Type   reflect.Type
	Params []Param
	// BodyIndex is the index path of a field named Body, or nil.
	BodyIndex []int
	// BodyType is the type the request body decodes into, or nil when the type
	// accepts no body.
	BodyType reflect.Type
	// WholeBody reports that the body decodes into the whole struct.
	WholeBody bool
}

// Source provides raw request values.
type Source interface {
	PathValue(name string) (string, bool)
	QueryValues() url.Values
	HeaderValues(name string) []string
	CookieValue(name string) (string, bool)
}

var plans sync.Map // reflect.Type -> *Plan or error

// PlanFor returns the cached plan for struct type t.
func PlanFor(t reflect.Type) (*Plan, error) {
	if v, ok := plans.Load(t); ok {
		if err, isErr := v.(error); isErr {
			return nil, err
		}
		return v.(*Plan), nil
	}
	p, err := compile(t)
	if err != nil {
		plans.Store(t, err)
		return nil, err
	}
	actual, _ := plans.LoadOrStore(t, p)
	if err, isErr := actual.(error); isErr {
		return nil, err
	}
	return actual.(*Plan), nil
}

// ParamTag returns the location and name of a parameter field.
func ParamTag(sf reflect.StructField) (Location, string, bool) {
	for _, loc := range locations {
		if tag, ok := sf.Tag.Lookup(string(loc)); ok {
			name, _, _ := strings.Cut(tag, ",")
			if name == "" {
				name = sf.Name
			}
			return loc, name, true
		}
	}
	return "", "", false
}

// IsParamField reports whether sf is bound from a request parameter.
func IsParamField(sf reflect.StructField) bool {
	_, _, ok := ParamTag(sf)
	return ok
}

func compile(t reflect.Type) (*Plan, error) {
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("binding: %s is not a struct", t)
	}
	p := &Plan{Type: t}
	hasBodyFields := false
	if err := collect(p, t, nil, &hasBodyFields); err != nil {
		return nil, err
	}
	if p.BodyIndex != nil {
		p.BodyType = t.FieldByIndex(p.BodyIndex).Type
	} else if hasBodyFields {
		p.WholeBody = true
		p.BodyType = t
	}
	return p, nil
}

func collect(p *Plan, t reflect.Type, prefix []int, hasBodyFields *bool) error {
	for i := range t.NumField() {
		sf := t.Field(i)
		index := append(append([]int(nil), prefix...), i)
		loc, name, isParam := ParamTag(sf)
		if !isParam && sf.Anonymous && sf.Type.Kind() == reflect.Struct {
			if err := collect(p, sf.Type, index, hasBodyFields); err != nil {
				return err
			}
			continue
		}
		if !sf.IsExported() {
			continue
		}
		if !isParam {
			if sf.Name == "Body" && len(prefix) == 0 {
				p.BodyIndex = index
			} else if sf.Tag.Get("json") != "-" {
				*hasBodyFields = true
			}
			continue
		}
		param := Param{Name: name, In: loc, Index: index, Field: sf}
		ft := sf.Type
		if ft.Kind() == reflect.Slice && ft.Elem().Kind() != reflect.Uint8 {
			param.multi = true
			ft = ft.Elem()
		}
		dec, err := decoderFor(ft)
		if err != nil {
			return fmt.Errorf("binding: %s.%s: %w", t, sf.Name, err)
		}
		param.decode = dec
		if def, ok := sf.Tag.Lookup("default"); ok {
			param.Default, param.HasDefault = def, true
			if err := dec(reflect.New(ft).Elem(), def); err != nil {
				return fmt.Errorf("binding: %s.%s: invalid default %q: %w", t, sf.Name, def, err)
			}
		}
		if loc == Header {
			param.Name = textproto.CanonicalMIMEHeaderKey(name)
		}
		p.Params = append(p.Params, param)
	}
	return nil
}

// Bind resets every parameter field of dst and fills it from src. dst must be
// an addressable struct value of the plan's type. Conversion failures are
// returned as validate.Errors so they render like validation failures.
func (p *Plan) Bind(dst reflect.Value, src Source) error {
	var errs validate.Errors
	var query url.Values
	for i := range p.Params {
		pr := &p.Params[i]
		fv := dst.FieldByIndex(pr.Index)
		fv.SetZero()
		var values []string
		switch pr.In {
		case Path:
			if v, ok := src.PathValue(pr.Name); ok {
				values = []string{v}
			}
		case Query:
			if query == nil {
				query = src.QueryValues()
			}
			values = query[pr.Name]
		case Header:
			values = src.HeaderValues(pr.Name)
		case Cookie:
			if v, ok := src.CookieValue(pr.Name); ok {
				values = []string{v}
			}
		}
		if len(values) == 0 {
			if !pr.HasDefault {
				continue
			}
			values = []string{pr.Default}
		}
		if err := pr.set(fv, values); err != nil {
			errs = append(errs, validate.FieldError{
				Field:   string(pr.In) + "." + pr.Name,
				Rule:    "type",
				Message: err.Error(),
			})
		}
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

func (pr *Param) set(fv reflect.Value, values []string) error {
	if !pr.multi {
		return pr.decode(fv, values[0])
	}
	s := reflect.MakeSlice(fv.Type(), len(values), len(values))
	for i, v := range values {
		if err := pr.decode(s.Index(i), v); err != nil {
			return err
		}
	}
	fv.Set(s)
	return nil
}

type decoder func(v reflect.Value, s string) error

var (
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	durationType        = reflect.TypeFor[time.Duration]()
)

func decoderFor(t reflect.Type) (decoder, error) {
	if t.Kind() == reflect.Pointer {
		elem, err := decoderFor(t.Elem())
		if err != nil {
			return nil, err
		}
		return func(v reflect.Value, s string) error {
			nv := reflect.New(t.Elem())
			if err := elem(nv.Elem(), s); err != nil {
				return err
			}
			v.Set(nv)
			return nil
		}, nil
	}
	if reflect.PointerTo(t).Implements(textUnmarshalerType) {
		return func(v reflect.Value, s string) error {
			if err := v.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(s)); err != nil {
				return fmt.Errorf("is not a valid %s", friendlyType(t))
			}
			return nil
		}, nil
	}
	if t == durationType {
		return func(v reflect.Value, s string) error {
			d, err := time.ParseDuration(s)
			if err != nil {
				return fmt.Errorf("must be a duration such as 1m30s")
			}
			v.SetInt(int64(d))
			return nil
		}, nil
	}
	switch t.Kind() {
	case reflect.String:
		return func(v reflect.Value, s string) error { v.SetString(s); return nil }, nil
	case reflect.Bool:
		return func(v reflect.Value, s string) error {
			b, err := strconv.ParseBool(s)
			if err != nil {
				return fmt.Errorf("must be a boolean")
			}
			v.SetBool(b)
			return nil
		}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		bits := t.Bits()
		return func(v reflect.Value, s string) error {
			n, err := strconv.ParseInt(s, 10, bits)
			if err != nil {
				return fmt.Errorf("must be an integer")
			}
			v.SetInt(n)
			return nil
		}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		bits := t.Bits()
		return func(v reflect.Value, s string) error {
			n, err := strconv.ParseUint(s, 10, bits)
			if err != nil {
				return fmt.Errorf("must be a non-negative integer")
			}
			v.SetUint(n)
			return nil
		}, nil
	case reflect.Float32, reflect.Float64:
		bits := t.Bits()
		return func(v reflect.Value, s string) error {
			n, err := strconv.ParseFloat(s, bits)
			if err != nil {
				return fmt.Errorf("must be a number")
			}
			v.SetFloat(n)
			return nil
		}, nil
	default:
		return nil, fmt.Errorf("unsupported parameter type %s", t)
	}
}

func friendlyType(t reflect.Type) string {
	if t.Name() == "Time" && t.PkgPath() == "time" {
		return "RFC 3339 timestamp"
	}
	return strings.ToLower(t.Name())
}
