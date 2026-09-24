// Package validate implements struct validation driven by `validate` struct
// tags.
//
// Validation plans are compiled once per type and cached, so validating a value
// only walks its fields and runs pre-parsed checks. Nested structs, pointers,
// slices, arrays and maps are validated recursively. Types may implement
// Validatable for custom cross-field validation.
//
//	type CreateUser struct {
//	    Name  string   `json:"name"  validate:"required,min=3,max=64"`
//	    Email string   `json:"email" validate:"required,email"`
//	    Tags  []string `json:"tags"  validate:"max=10,dive,min=1"`
//	    Code  string   `json:"code"  pattern:"^[A-Z]{3}$"`
//	}
//
// The same tags drive the OpenAPI schema generator, so constraints are declared
// exactly once.
package validate

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// FieldError describes a single failed validation rule.
type FieldError struct {
	// Field is the path to the failing value using wire names, for example
	// "items[2].name".
	Field string `json:"field"`
	// Rule is the name of the failing rule, for example "min".
	Rule string `json:"rule"`
	// Param is the rule parameter, for example "3" for min=3.
	Param string `json:"param,omitempty"`
	// Message is a human-readable description of the failure.
	Message string `json:"message"`
}

// Errors is a list of validation failures. It is returned by Struct when one or
// more rules fail.
type Errors []FieldError

func (e Errors) Error() string {
	var b strings.Builder
	for i, fe := range e {
		if i > 0 {
			b.WriteString("; ")
		}
		if fe.Field != "" {
			b.WriteString(fe.Field)
			b.WriteString(": ")
		}
		b.WriteString(fe.Message)
	}
	return b.String()
}

// Validatable is implemented by types that need custom validation beyond struct
// tags. Validate runs after tag rules succeed for the value's own fields.
// Returning Errors or a FieldError reports field-level failures; any other
// error is reported as a failure of the value itself.
type Validatable interface {
	Validate() error
}

// RuleFunc reports whether v satisfies a custom rule. Pointers are already
// dereferenced; nil pointers never reach a rule.
type RuleFunc func(v reflect.Value, param string) bool

// Rule is a parsed tag rule.
type Rule struct {
	Name  string
	Param string
}

// ParseTag parses a validate tag such as "required,min=3" into rules.
func ParseTag(tag string) ([]Rule, error) {
	if tag == "" {
		return nil, nil
	}
	parts := strings.Split(tag, ",")
	rules := make([]Rule, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		name, param, _ := strings.Cut(p, "=")
		if name == "" {
			return nil, fmt.Errorf("validate: empty rule name in tag %q", tag)
		}
		rules = append(rules, Rule{Name: name, Param: param})
	}
	return rules, nil
}

// Validator validates structs. It is safe for concurrent use. The zero value is
// not usable; call New.
type Validator struct {
	mu        sync.RWMutex
	factories map[string]ruleFactory
	messages  map[string]messageFunc
	plans     sync.Map // reflect.Type -> *typePlan
	compileMu sync.Mutex
}

// Default is the validator used by the package-level functions and by Torge
// unless another validator is configured.
var Default = New()

// Struct validates v with the Default validator.
func Struct(v any) error { return Default.Struct(v) }

// New returns a Validator with the built-in rules registered.
func New() *Validator {
	v := &Validator{
		factories: make(map[string]ruleFactory, len(builtinRules)),
		messages:  make(map[string]messageFunc, len(builtinRules)),
	}
	for name, r := range builtinRules {
		v.factories[name] = r.factory
		v.messages[name] = r.message
	}
	return v
}

// RegisterRule adds or replaces a custom rule. message may contain %s, which is
// replaced by the rule parameter. Rules must be registered before types using
// them are first validated.
func (v *Validator) RegisterRule(name string, fn RuleFunc, message string) error {
	if name == "" || fn == nil {
		return errors.New("validate: rule name and function are required")
	}
	if name == "omitempty" || name == "dive" || name == "required" {
		return fmt.Errorf("validate: rule %q is reserved", name)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.factories[name] = func(param string, _ reflect.Type) (checkFunc, error) {
		return func(rv reflect.Value) bool { return fn(rv, param) }, nil
	}
	v.messages[name] = static(message)
	return nil
}

// Validate implements the validator interface used by Torge.
func (v *Validator) Validate(x any) error { return v.Struct(x) }

// Struct validates x, which must be a struct or a pointer to a struct. It
// returns nil, Errors, or a non-validation error if x's type has invalid tags.
func (v *Validator) Struct(x any) error {
	rv := reflect.ValueOf(x)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return errors.New("validate: nil value")
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return fmt.Errorf("validate: expected struct, got %s", rv.Kind())
	}
	plan, err := v.plan(rv.Type())
	if err != nil {
		return err
	}
	var errs Errors
	v.validateStruct(rv, plan, "", &errs)
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// Compile compiles and caches the validation plan for t, reporting invalid
// tags. Frameworks call it at startup so that tag mistakes fail early.
func (v *Validator) Compile(t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	_, err := v.plan(t)
	return err
}

type checkFunc func(reflect.Value) bool

type ruleFactory func(param string, t reflect.Type) (checkFunc, error)

type check struct {
	rule    string
	param   string
	message string
	fn      checkFunc
}

// valuePlan is the set of checks applied to one value (a field, or an element
// after "dive").
type valuePlan struct {
	required  bool
	omitempty bool
	checks    []check
	dive      *valuePlan
}

type fieldPlan struct {
	index  int
	name   string
	checks valuePlan
	// nested is true when the field's type may contain validated structs.
	nested bool
}

type typePlan struct {
	fields      []fieldPlan
	validatable bool
	hasWork     bool
}

var validatableType = reflect.TypeFor[Validatable]()

func (v *Validator) plan(t reflect.Type) (*typePlan, error) {
	if p, ok := v.plans.Load(t); ok {
		return p.(*typePlan), nil
	}
	v.compileMu.Lock()
	defer v.compileMu.Unlock()
	if p, ok := v.plans.Load(t); ok {
		return p.(*typePlan), nil
	}
	building := make(map[reflect.Type]*typePlan)
	p, err := v.compile(t, building)
	if err != nil {
		return nil, err
	}
	for bt, bp := range building {
		v.plans.Store(bt, bp)
	}
	return p, nil
}

func (v *Validator) compile(t reflect.Type, building map[reflect.Type]*typePlan) (*typePlan, error) {
	if p, ok := v.plans.Load(t); ok {
		return p.(*typePlan), nil
	}
	if p, ok := building[t]; ok {
		return p, nil
	}
	p := &typePlan{
		validatable: t.Implements(validatableType) || reflect.PointerTo(t).Implements(validatableType),
	}
	// Assume work while compiling so recursive types are traversed.
	p.hasWork = true
	building[t] = p

	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() && !sf.Anonymous {
			continue
		}
		fp := fieldPlan{index: i, name: FieldName(sf)}
		tag := sf.Tag.Get("validate")
		if tag == "-" {
			continue
		}
		vp, err := v.compileValuePlan(tag, sf.Type)
		if err != nil {
			return nil, fmt.Errorf("validate: %s.%s: %w", t.String(), sf.Name, err)
		}
		if pattern, ok := sf.Tag.Lookup("pattern"); ok {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("validate: %s.%s: invalid pattern: %w", t.String(), sf.Name, err)
			}
			vp.checks = append(vp.checks, check{
				rule: "pattern", param: pattern,
				message: "must match pattern " + pattern,
				fn: func(rv reflect.Value) bool {
					return rv.Kind() != reflect.String || re.MatchString(rv.String())
				},
			})
		}
		fp.checks = vp
		nested, err := v.mayNeedValidation(sf.Type, building)
		if err != nil {
			return nil, err
		}
		fp.nested = nested
		if fp.nested || vp.required || len(vp.checks) > 0 || vp.dive != nil {
			p.fields = append(p.fields, fp)
		}
	}
	p.hasWork = len(p.fields) > 0 || p.validatable
	return p, nil
}

func (v *Validator) mayNeedValidation(t reflect.Type, building map[reflect.Type]*typePlan) (bool, error) {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		return v.mayNeedValidation(t.Elem(), building)
	case reflect.Struct:
		p, err := v.compile(t, building)
		if err != nil {
			return false, err
		}
		return p.hasWork, nil
	default:
		return false, nil
	}
}

func (v *Validator) compileValuePlan(tag string, t reflect.Type) (valuePlan, error) {
	rules, err := ParseTag(tag)
	if err != nil {
		return valuePlan{}, err
	}
	return v.compileRules(rules, t)
}

func (v *Validator) compileRules(rules []Rule, t reflect.Type) (valuePlan, error) {
	var vp valuePlan
	base := t
	for base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	for i, r := range rules {
		switch r.Name {
		case "required":
			vp.required = true
			continue
		case "omitempty":
			vp.omitempty = true
			continue
		case "dive":
			switch base.Kind() {
			case reflect.Slice, reflect.Array, reflect.Map:
			default:
				return vp, fmt.Errorf("dive requires a slice, array or map, got %s", base)
			}
			elem, err := v.compileRules(rules[i+1:], base.Elem())
			if err != nil {
				return vp, err
			}
			vp.dive = &elem
			return vp, nil
		}
		factory, msg, ok := v.lookupRule(r.Name)
		if !ok {
			return vp, fmt.Errorf("unknown rule %q", r.Name)
		}
		fn, err := factory(r.Param, base)
		if err != nil {
			return vp, fmt.Errorf("rule %q: %w", r.Name, err)
		}
		vp.checks = append(vp.checks, check{rule: r.Name, param: r.Param, message: msg(r.Param, base), fn: fn})
	}
	return vp, nil
}

func (v *Validator) lookupRule(name string) (ruleFactory, messageFunc, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	f, ok := v.factories[name]
	return f, v.messages[name], ok
}

func (v *Validator) validateStruct(rv reflect.Value, p *typePlan, path string, errs *Errors) {
	for i := range p.fields {
		fp := &p.fields[i]
		fv := rv.Field(fp.index)
		fpath := joinPath(path, fp.name)
		if !v.applyValuePlan(fv, &fp.checks, fpath, errs) {
			continue
		}
		if fp.nested {
			v.validateNested(fv, fpath, errs)
		}
	}
	if p.validatable {
		v.runValidatable(rv, path, errs)
	}
}

// applyValuePlan runs the checks for one value. It returns false if the value
// is absent or failed a check, in which case nested validation is skipped.
func (v *Validator) applyValuePlan(fv reflect.Value, vp *valuePlan, path string, errs *Errors) bool {
	dv, present := deref(fv)
	if !present || (vp.omitempty && dv.IsZero()) {
		if vp.required {
			*errs = append(*errs, FieldError{Field: path, Rule: "required", Message: "is required"})
		}
		return false
	}
	if vp.required && isEmpty(dv) {
		*errs = append(*errs, FieldError{Field: path, Rule: "required", Message: "is required"})
		return false
	}
	// Report only the first failing rule per value: later rules usually
	// restate the same problem.
	for _, c := range vp.checks {
		if !c.fn(dv) {
			*errs = append(*errs, FieldError{Field: path, Rule: c.rule, Param: c.param, Message: c.message})
			return false
		}
	}
	if vp.dive != nil {
		switch dv.Kind() {
		case reflect.Slice, reflect.Array:
			for i := range dv.Len() {
				v.applyValuePlan(dv.Index(i), vp.dive, path+"["+strconv.Itoa(i)+"]", errs)
			}
		case reflect.Map:
			iter := dv.MapRange()
			for iter.Next() {
				v.applyValuePlan(iter.Value(), vp.dive, path+"["+fmt.Sprint(iter.Key().Interface())+"]", errs)
			}
		}
	}
	return true
}

func (v *Validator) validateNested(fv reflect.Value, path string, errs *Errors) {
	dv, present := deref(fv)
	if !present {
		return
	}
	switch dv.Kind() {
	case reflect.Struct:
		p, err := v.plan(dv.Type())
		if err != nil || !p.hasWork {
			return
		}
		v.validateStruct(dv, p, path, errs)
	case reflect.Slice, reflect.Array:
		for i := range dv.Len() {
			v.validateNested(dv.Index(i), path+"["+strconv.Itoa(i)+"]", errs)
		}
	case reflect.Map:
		iter := dv.MapRange()
		for iter.Next() {
			v.validateNested(iter.Value(), path+"["+fmt.Sprint(iter.Key().Interface())+"]", errs)
		}
	}
}

func (v *Validator) runValidatable(rv reflect.Value, path string, errs *Errors) {
	var target any
	if rv.CanAddr() {
		target = rv.Addr().Interface()
	} else {
		target = rv.Interface()
	}
	val, ok := target.(Validatable)
	if !ok {
		return
	}
	err := val.Validate()
	if err == nil {
		return
	}
	var fes Errors
	var fe FieldError
	var fep *FieldError
	switch {
	case errors.As(err, &fes):
		for _, e := range fes {
			e.Field = joinPath(path, e.Field)
			*errs = append(*errs, e)
		}
	case errors.As(err, &fep):
		e := *fep
		e.Field = joinPath(path, e.Field)
		*errs = append(*errs, e)
	case errors.As(err, &fe):
		fe.Field = joinPath(path, fe.Field)
		*errs = append(*errs, fe)
	default:
		*errs = append(*errs, FieldError{Field: path, Rule: "custom", Message: err.Error()})
	}
}

// Error implements error so that a single FieldError can be returned from
// Validatable.Validate.
func (e FieldError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return e.Field + ": " + e.Message
}

func joinPath(base, name string) string {
	switch {
	case base == "":
		return name
	case name == "":
		return base
	default:
		return base + "." + name
	}
}

func deref(v reflect.Value) (reflect.Value, bool) {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return v, false
		}
		v = v.Elem()
	}
	return v, v.IsValid()
}

func isEmpty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Slice, reflect.Map, reflect.String, reflect.Array:
		return v.Len() == 0
	default:
		return v.IsZero()
	}
}

// FieldName returns the wire name of a struct field: the json tag name, else
// the first binding tag name (path, query, header, cookie, form), else the Go
// field name.
func FieldName(sf reflect.StructField) string {
	if name := tagName(sf.Tag.Get("json")); name != "" && name != "-" {
		return name
	}
	for _, key := range [...]string{"path", "query", "header", "cookie", "form"} {
		if name := tagName(sf.Tag.Get(key)); name != "" && name != "-" {
			return name
		}
	}
	return sf.Name
}

func tagName(tag string) string {
	name, _, _ := strings.Cut(tag, ",")
	return name
}
