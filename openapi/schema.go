package openapi

import (
	"encoding"
	"encoding/json"
	"path"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/TosmimForidMehtab/torge/validate"
)

// Generator derives JSON Schemas from Go types. Named struct types are emitted
// once as components and referenced with $ref. A Generator is not safe for
// concurrent use.
type Generator struct {
	schemas map[string]*Schema
	names   map[reflect.Type]string
	owners  map[string]reflect.Type
}

// NewGenerator returns an empty Generator.
func NewGenerator() *Generator {
	return &Generator{
		schemas: make(map[string]*Schema),
		names:   make(map[reflect.Type]string),
		owners:  make(map[string]reflect.Type),
	}
}

// Schemas returns the component schemas generated so far.
func (g *Generator) Schemas() map[string]*Schema { return g.schemas }

// Schema returns the schema for t. Named structs are returned as references.
func (g *Generator) Schema(t reflect.Type) *Schema {
	return g.schema(t, true)
}

// InlineSchema returns the schema for struct type t without registering it as
// a component, omitting fields for which exclude returns true.
func (g *Generator) InlineSchema(t reflect.Type, exclude func(reflect.StructField) bool) *Schema {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return g.Schema(t)
	}
	return g.structSchema(t, exclude)
}

// RefName returns the component name for a schema reference, or "".
func RefName(s *Schema) string {
	if s == nil {
		return ""
	}
	return strings.TrimPrefix(s.Ref, "#/components/schemas/")
}

var (
	timeType          = reflect.TypeFor[time.Time]()
	durationType      = reflect.TypeFor[time.Duration]()
	rawMessageType    = reflect.TypeFor[json.RawMessage]()
	byteSliceType     = reflect.TypeFor[[]byte]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	providerType      = reflect.TypeFor[SchemaProvider]()
)

func implements(t, iface reflect.Type) bool {
	return t.Implements(iface) || (t.Kind() != reflect.Pointer && reflect.PointerTo(t).Implements(iface))
}

func (g *Generator) schema(t reflect.Type, allowRef bool) *Schema {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if implements(t, providerType) {
		return reflect.New(t).Interface().(SchemaProvider).OpenAPISchema()
	}
	switch t {
	case timeType:
		return &Schema{Type: "string", Format: "date-time"}
	case durationType:
		return &Schema{Type: "integer", Format: "int64", Description: "Duration in nanoseconds"}
	case rawMessageType:
		return &Schema{}
	case byteSliceType:
		return &Schema{Type: "string", Format: "byte"}
	}
	if implements(t, textMarshalerType) {
		return &Schema{Type: "string"}
	}
	if implements(t, jsonMarshalerType) {
		return &Schema{}
	}
	switch t.Kind() {
	case reflect.Bool:
		return &Schema{Type: "boolean"}
	case reflect.Int, reflect.Int64:
		return &Schema{Type: "integer", Format: "int64"}
	case reflect.Int8, reflect.Int16, reflect.Int32:
		return &Schema{Type: "integer", Format: "int32"}
	case reflect.Uint, reflect.Uint64, reflect.Uintptr:
		return &Schema{Type: "integer", Format: "int64", Minimum: ptr(0.0)}
	case reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return &Schema{Type: "integer", Format: "int32", Minimum: ptr(0.0)}
	case reflect.Float32:
		return &Schema{Type: "number", Format: "float"}
	case reflect.Float64:
		return &Schema{Type: "number", Format: "double"}
	case reflect.String:
		return &Schema{Type: "string"}
	case reflect.Slice, reflect.Array:
		s := &Schema{Type: "array", Items: g.schema(t.Elem(), true)}
		if t.Kind() == reflect.Array {
			s.MinItems, s.MaxItems = ptr(t.Len()), ptr(t.Len())
		}
		return s
	case reflect.Map:
		return &Schema{Type: "object", AdditionalProperties: g.schema(t.Elem(), true)}
	case reflect.Struct:
		if !allowRef || t.Name() == "" {
			return g.structSchema(t, nil)
		}
		return g.ref(t)
	default:
		// Interfaces, funcs and channels: accept anything.
		return &Schema{}
	}
}

func (g *Generator) ref(t reflect.Type) *Schema {
	if name, ok := g.names[t]; ok {
		return &Schema{Ref: "#/components/schemas/" + name}
	}
	name := g.componentName(t)
	g.names[t] = name
	g.owners[name] = t
	placeholder := &Schema{}
	g.schemas[name] = placeholder
	*placeholder = *g.structSchema(t, nil)
	return &Schema{Ref: "#/components/schemas/" + name}
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (g *Generator) componentName(t reflect.Type) string {
	name := sanitizeName(t.Name())
	if owner, ok := g.owners[name]; !ok || owner == t {
		return name
	}
	qualified := sanitizeName(path.Base(t.PkgPath()) + "." + t.Name())
	for i := 2; ; i++ {
		if owner, ok := g.owners[qualified]; !ok || owner == t {
			return qualified
		}
		qualified = sanitizeName(path.Base(t.PkgPath())+"."+t.Name()) + strconv.Itoa(i)
	}
}

func sanitizeName(name string) string {
	name = strings.NewReplacer("[", "_", "]", "", "*", "", " ", "").Replace(name)
	return strings.Trim(unsafeName.ReplaceAllString(name, "_"), "_")
}

func (g *Generator) structSchema(t reflect.Type, exclude func(reflect.StructField) bool) *Schema {
	s := &Schema{Type: "object", Properties: make(map[string]*Schema)}
	g.addFields(s, t, exclude)
	if len(s.Properties) == 0 {
		s.Properties = nil
	}
	return s
}

func (g *Generator) addFields(s *Schema, t reflect.Type, exclude func(reflect.StructField) bool) {
	for i := range t.NumField() {
		sf := t.Field(i)
		jsonTag := sf.Tag.Get("json")
		if jsonTag == "-" || (exclude != nil && exclude(sf)) {
			continue
		}
		name, _, _ := strings.Cut(jsonTag, ",")
		if sf.Anonymous && name == "" {
			et := sf.Type
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				g.addFields(s, et, exclude)
				continue
			}
		}
		if !sf.IsExported() {
			continue
		}
		if name == "" {
			name = sf.Name
		}
		fs := g.schema(sf.Type, true)
		required := g.applyTags(fs, sf)
		s.Properties[name] = fs
		if required {
			s.Required = append(s.Required, name)
		}
	}
}

// applyTags decorates fs with documentation and validation constraints from
// the field's tags. It reports whether the field is required.
func (g *Generator) applyTags(fs *Schema, sf reflect.StructField) bool {
	FieldDoc(fs, sf)
	rules, _ := validate.ParseTag(sf.Tag.Get("validate"))
	required := false
	target := fs
	base := sf.Type
	for base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	for _, r := range rules {
		if r.Name == "required" {
			required = true
			continue
		}
		if r.Name == "dive" {
			if target.Items == nil && target.AdditionalProperties == nil {
				break
			}
			if target.Items != nil {
				target = target.Items
			} else {
				target = target.AdditionalProperties
			}
			base = base.Elem()
			for base.Kind() == reflect.Pointer {
				base = base.Elem()
			}
			continue
		}
		ApplyRule(target, base, r)
	}
	if p, ok := sf.Tag.Lookup("pattern"); ok && fs.Ref == "" {
		fs.Pattern = p
	}
	return required
}

// FieldDoc applies documentation tags (doc, example, default, format,
// deprecated, readonly, writeonly) to s.
func FieldDoc(s *Schema, sf reflect.StructField) {
	if d := sf.Tag.Get("doc"); d != "" {
		s.Description = d
	}
	if f := sf.Tag.Get("format"); f != "" {
		s.Format = f
	}
	base := sf.Type
	for base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	if ex, ok := sf.Tag.Lookup("example"); ok {
		s.Examples = []any{ParseValue(ex, base)}
	}
	if def, ok := sf.Tag.Lookup("default"); ok {
		s.Default = ParseValue(def, base)
	}
	if sf.Tag.Get("deprecated") == "true" {
		s.Deprecated = true
	}
	if sf.Tag.Get("readonly") == "true" {
		s.ReadOnly = true
	}
	if sf.Tag.Get("writeonly") == "true" {
		s.WriteOnly = true
	}
}

// ApplyRule maps a validation rule onto schema constraints. Unknown rules are
// ignored because they cannot be expressed in JSON Schema.
func ApplyRule(s *Schema, t reflect.Type, r validate.Rule) {
	if s.Ref != "" {
		return
	}
	kind := t.Kind()
	isString := kind == reflect.String
	isArray := kind == reflect.Slice || kind == reflect.Array
	isMap := kind == reflect.Map
	num, numErr := strconv.ParseFloat(r.Param, 64)
	if t == durationType {
		if d, err := time.ParseDuration(r.Param); err == nil {
			num, numErr = float64(d), nil
		}
	}
	intParam := int(num)
	switch r.Name {
	case "min", "gte", "max", "lte", "len":
		if numErr != nil {
			return
		}
		lower := r.Name == "min" || r.Name == "gte" || r.Name == "len"
		upper := r.Name == "max" || r.Name == "lte" || r.Name == "len"
		switch {
		case isString:
			if lower {
				s.MinLength = ptr(intParam)
			}
			if upper {
				s.MaxLength = ptr(intParam)
			}
		case isArray:
			if lower {
				s.MinItems = ptr(intParam)
			}
			if upper {
				s.MaxItems = ptr(intParam)
			}
		case isMap:
			if lower {
				s.MinProperties = ptr(intParam)
			}
			if upper {
				s.MaxProperties = ptr(intParam)
			}
		default:
			if lower {
				s.Minimum = ptr(num)
			}
			if upper {
				s.Maximum = ptr(num)
			}
		}
	case "gt":
		if numErr == nil && !isString && !isArray && !isMap {
			s.ExclusiveMinimum = ptr(num)
		}
	case "lt":
		if numErr == nil && !isString && !isArray && !isMap {
			s.ExclusiveMaximum = ptr(num)
		}
	case "oneof":
		for _, o := range strings.Fields(r.Param) {
			s.Enum = append(s.Enum, ParseValue(o, t))
		}
	case "eq":
		s.Const = ParseValue(r.Param, t)
	case "email":
		s.Format = "email"
	case "url":
		s.Format = "uri"
	case "uuid":
		s.Format = "uuid"
	case "ipv4":
		s.Format = "ipv4"
	case "ipv6":
		s.Format = "ipv6"
	case "alpha":
		s.Pattern = `^\p{L}*$`
	case "alphanum":
		s.Pattern = `^[\p{L}\p{N}]*$`
	}
}

// ParseValue converts a tag string to a value of the kind of t for use in
// examples, defaults and enums.
func ParseValue(v string, t reflect.Type) any {
	switch t.Kind() {
	case reflect.String:
		return v
	case reflect.Bool:
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return n
		}
	case reflect.Float32, reflect.Float64:
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			return n
		}
	default:
		var out any
		if json.Unmarshal([]byte(v), &out) == nil {
			return out
		}
	}
	return v
}

func ptr[T any](v T) *T { return &v }
