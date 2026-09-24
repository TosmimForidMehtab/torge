// Package config loads typed configuration from environment variables.
//
//	type Config struct {
//	    Port        int           `env:"PORT" default:"8080" validate:"min=1,max=65535"`
//	    DatabaseURL config.Secret `env:"DATABASE_URL,required"`
//	    Timeout     time.Duration `env:"TIMEOUT" default:"5s"`
//	    Redis       RedisConfig   `envPrefix:"REDIS_"`
//	}
//
//	var cfg Config
//	if err := config.Load(&cfg); err != nil { ... }
//
// Load reports every problem at once (missing required values, unparsable
// values, failed validation rules) so misconfiguration fails fast with an
// actionable message before a server accepts traffic.
//
// Nested structs share their parent's variables, optionally prefixed with an
// envPrefix tag. A pointer-to-struct field is an optional section: it stays
// nil unless at least one of its variables is set.
//
// Values of type Secret are never printed by fmt, encoding/json or log/slog,
// and Redacted masks both Secret values and fields tagged with the "secret"
// option.
package config

import (
	"encoding"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TosmimForidMehtab/torge/validate"
)

// Env is the deployment environment.
type Env string

// Deployment environments.
const (
	Development Env = "development"
	Test        Env = "test"
	Production  Env = "production"
)

// IsProduction reports whether e is Production.
func (e Env) IsProduction() bool { return e == Production }

// IsDevelopment reports whether e is Development.
func (e Env) IsDevelopment() bool { return e == Development }

// IsTest reports whether e is Test.
func (e Env) IsTest() bool { return e == Test }

// ParseEnv parses an environment name. Common aliases (dev, prod, testing) are
// accepted.
func ParseEnv(s string) (Env, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "development", "dev", "local":
		return Development, nil
	case "test", "testing":
		return Test, nil
	case "production", "prod":
		return Production, nil
	default:
		return "", fmt.Errorf("config: unknown environment %q (want development, test or production)", s)
	}
}

// EnvVars lists the variables consulted by CurrentEnv, in order.
var EnvVars = []string{"TORGE_ENV", "APP_ENV"}

// CurrentEnv returns the environment named by TORGE_ENV or APP_ENV. When
// neither is set it returns Production, so that forgetting to configure the
// environment never enables development behavior in a deployment. An invalid
// value is reported as an error together with Production.
func CurrentEnv() (Env, error) {
	for _, name := range EnvVars {
		if v, ok := os.LookupEnv(name); ok && v != "" {
			env, err := ParseEnv(v)
			if err != nil {
				return Production, fmt.Errorf("%s: %w", name, err)
			}
			return env, nil
		}
	}
	return Production, nil
}

// Secret is a string that is redacted whenever it is formatted, marshaled or
// logged. Call Value to obtain the underlying string.
type Secret string

const redacted = "[REDACTED]"

// Value returns the secret value.
func (s Secret) Value() string { return string(s) }

// String implements fmt.Stringer and always returns a redacted placeholder.
func (s Secret) String() string { return redacted }

// GoString implements fmt.GoStringer.
func (s Secret) GoString() string { return redacted }

// MarshalJSON implements json.Marshaler with a redacted placeholder.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// MarshalText implements encoding.TextMarshaler with a redacted placeholder.
func (s Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (s *Secret) UnmarshalText(b []byte) error { *s = Secret(b); return nil }

// Problem is a single configuration problem.
type Problem struct {
	// Var is the environment variable name, if any.
	Var string
	// Field is the Go field path.
	Field string
	// Message explains the problem.
	Message string
}

// Error reports all configuration problems found by Load.
type Error struct {
	Problems []Problem
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "config: %d problem(s):", len(e.Problems))
	for _, p := range e.Problems {
		b.WriteString("\n  - ")
		if p.Var != "" {
			b.WriteString(p.Var)
			b.WriteString(" (")
			b.WriteString(p.Field)
			b.WriteString("): ")
		} else {
			b.WriteString(p.Field)
			b.WriteString(": ")
		}
		b.WriteString(p.Message)
	}
	return b.String()
}

// Option configures Load.
type Option func(*loader)

// WithPrefix prepends prefix to every variable name, for example "MYAPP_".
func WithPrefix(prefix string) Option {
	return func(l *loader) { l.prefix = prefix }
}

// WithLookup replaces os.LookupEnv, which is useful in tests.
func WithLookup(lookup func(string) (string, bool)) Option {
	return func(l *loader) { l.lookup = lookup }
}

// WithMap reads variables from m instead of the process environment.
func WithMap(m map[string]string) Option {
	return WithLookup(func(k string) (string, bool) { v, ok := m[k]; return v, ok })
}

// WithValidator replaces the validator used for `validate` tags.
func WithValidator(v *validate.Validator) Option {
	return func(l *loader) { l.validator = v }
}

type loader struct {
	prefix    string
	lookup    func(string) (string, bool)
	validator *validate.Validator
	problems  []Problem
	// found counts variables present in the environment.
	found int
	// vars maps validation paths to variable names for error reporting.
	vars map[string]string
}

// Load populates dst, which must be a non-nil pointer to a struct, from the
// environment and validates it.
func Load(dst any, opts ...Option) error {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return errors.New("config: Load requires a non-nil pointer to a struct")
	}
	l := &loader{lookup: os.LookupEnv, validator: validate.Default, vars: make(map[string]string)}
	for _, o := range opts {
		o(l)
	}
	if err := l.loadStruct(rv.Elem(), l.prefix, "", ""); err != nil {
		return err
	}
	// Validate even when loading found problems, so every problem is
	// reported at once; skip rules for variables already reported.
	reported := make(map[string]bool, len(l.problems))
	for _, p := range l.problems {
		reported[p.Var] = true
	}
	if err := l.validator.Struct(dst); err != nil {
		var verrs validate.Errors
		if !errors.As(err, &verrs) {
			return fmt.Errorf("config: %w", err)
		}
		for _, fe := range verrs {
			name := l.vars[fe.Field]
			if name != "" && reported[name] {
				continue
			}
			l.problems = append(l.problems, Problem{Var: name, Field: fe.Field, Message: fe.Message})
		}
	}
	if len(l.problems) > 0 {
		return &Error{Problems: l.problems}
	}
	return nil
}

var (
	durationType        = reflect.TypeFor[time.Duration]()
	urlType             = reflect.TypeFor[url.URL]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

func (l *loader) loadStruct(v reflect.Value, prefix, goPath, valPath string) error {
	t := v.Type()
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		fv := v.Field(i)
		fieldGoPath := joinPath(goPath, sf.Name)
		fieldValPath := joinPath(valPath, validate.FieldName(sf))
		tag, hasTag := sf.Tag.Lookup("env")
		if !hasTag {
			if !isNestedStruct(sf.Type) {
				continue
			}
			nestedPrefix := prefix + sf.Tag.Get("envPrefix")
			if sf.Type.Kind() != reflect.Pointer {
				if err := l.loadStruct(fv, nestedPrefix, fieldGoPath, fieldValPath); err != nil {
					return err
				}
				continue
			}
			// A pointer section is optional: it is populated, and its
			// required values enforced, only if one of its variables is set.
			sub := &loader{lookup: l.lookup, validator: l.validator, vars: l.vars}
			nv := reflect.New(sf.Type.Elem())
			if err := sub.loadStruct(nv.Elem(), nestedPrefix, fieldGoPath, fieldValPath); err != nil {
				return err
			}
			if sub.found > 0 {
				fv.Set(nv)
				l.found += sub.found
				l.problems = append(l.problems, sub.problems...)
			}
			continue
		}
		name, flags, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		name = prefix + name
		l.vars[fieldValPath] = name
		required := hasFlag(flags, "required")

		raw, ok := l.lookup(name)
		if ok && raw != "" {
			l.found++
		}
		if !ok || raw == "" {
			if def, hasDef := sf.Tag.Lookup("default"); hasDef {
				raw, ok = def, true
			}
		}
		if !ok || raw == "" {
			if required {
				l.problems = append(l.problems, Problem{Var: name, Field: fieldGoPath, Message: "required but not set"})
			}
			continue
		}
		if err := setValue(fv, raw, sf.Tag.Get("sep")); err != nil {
			l.problems = append(l.problems, Problem{Var: name, Field: fieldGoPath, Message: err.Error()})
		}
	}
	return nil
}

func isNestedStruct(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind() == reflect.Struct && t != urlType && !reflect.PointerTo(t).Implements(textUnmarshalerType)
}

func hasFlag(flags, want string) bool {
	for f := range strings.SplitSeq(flags, ",") {
		if strings.TrimSpace(f) == want {
			return true
		}
	}
	return false
}

func joinPath(base, name string) string {
	if base == "" {
		return name
	}
	return base + "." + name
}

func setValue(v reflect.Value, raw, sep string) error {
	t := v.Type()
	if t.Kind() == reflect.Pointer {
		nv := reflect.New(t.Elem())
		if err := setValue(nv.Elem(), raw, sep); err != nil {
			return err
		}
		v.Set(nv)
		return nil
	}
	if t == urlType {
		u, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("invalid URL")
		}
		v.Set(reflect.ValueOf(*u))
		return nil
	}
	if reflect.PointerTo(t).Implements(textUnmarshalerType) {
		if err := v.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(raw)); err != nil {
			return fmt.Errorf("invalid value: %v", err)
		}
		return nil
	}
	if t == durationType {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("invalid duration %q (examples: 30s, 5m, 1h)", raw)
		}
		v.SetInt(int64(d))
		return nil
	}
	switch t.Kind() {
	case reflect.String:
		v.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("invalid boolean %q (use true or false)", raw)
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, t.Bits())
		if err != nil {
			return fmt.Errorf("invalid integer %q", raw)
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, t.Bits())
		if err != nil {
			return fmt.Errorf("invalid unsigned integer %q", raw)
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(raw, t.Bits())
		if err != nil {
			return fmt.Errorf("invalid number %q", raw)
		}
		v.SetFloat(n)
	case reflect.Slice:
		if sep == "" {
			sep = ","
		}
		parts := strings.Split(raw, sep)
		s := reflect.MakeSlice(t, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			ev := reflect.New(t.Elem()).Elem()
			if err := setValue(ev, p, sep); err != nil {
				return err
			}
			s = reflect.Append(s, ev)
		}
		v.Set(s)
	default:
		return fmt.Errorf("unsupported type %s", t)
	}
	return nil
}

// Entry is one configuration value as reported by Redacted.
type Entry struct {
	Var   string
	Field string
	Value string
}

// Redacted returns the environment-backed fields of cfg with secrets masked,
// sorted by variable name. It is safe to log.
func Redacted(cfg any) []Entry {
	rv := reflect.ValueOf(cfg)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}
	var out []Entry
	collectEntries(rv, "", "", &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Var < out[j].Var })
	return out
}

var secretType = reflect.TypeFor[Secret]()

func collectEntries(v reflect.Value, prefix, goPath string, out *[]Entry) {
	t := v.Type()
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		fv := v.Field(i)
		path := joinPath(goPath, sf.Name)
		tag, ok := sf.Tag.Lookup("env")
		if !ok {
			if isNestedStruct(sf.Type) {
				if sf.Type.Kind() == reflect.Pointer {
					if fv.IsNil() {
						continue
					}
					fv = fv.Elem()
				}
				collectEntries(fv, prefix+sf.Tag.Get("envPrefix"), path, out)
			}
			continue
		}
		name, flags, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		value := fmt.Sprint(fv.Interface())
		if sf.Type == secretType || hasFlag(flags, "secret") {
			value = redacted
			if fv.IsZero() {
				value = ""
			}
		}
		*out = append(*out, Entry{Var: prefix + name, Field: path, Value: value})
	}
}
