package validate_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge/validate"
)

type Address struct {
	Street string `json:"street" validate:"required"`
	Zip    string `json:"zip" validate:"len=5,numeric"`
}

type Account struct {
	Name     string            `json:"name" validate:"required,min=2,max=10"`
	Email    string            `json:"email" validate:"omitempty,email"`
	Age      int               `json:"age" validate:"gte=18,lte=130"`
	Score    float64           `json:"score" validate:"gt=0,lt=1"`
	Role     string            `json:"role" validate:"oneof=admin member"`
	Level    int               `json:"level" validate:"oneof=1 2 3"`
	Tags     []string          `json:"tags" validate:"min=1,dive,alphanum,max=5"`
	Home     *Address          `json:"home"`
	Others   []Address         `json:"others"`
	Labels   map[string]string `json:"labels" validate:"max=2,dive,required"`
	Website  string            `json:"website" validate:"omitempty,url"`
	ID       string            `json:"id" validate:"omitempty,uuid"`
	Timeout  time.Duration     `json:"timeout" validate:"omitempty,max=1m"`
	Code     string            `json:"code" pattern:"^[A-Z]{3}$"`
	Nickname *string           `json:"nickname" validate:"omitempty,min=3"`
	Ignored  string            `validate:"-"`
	//lint:ignore U1000 verifies that unexported fields are skipped
	unexposed string
}

func valid() Account {
	return Account{
		Name: "Ada", Email: "ada@example.com", Age: 36, Score: 0.5, Role: "admin", Level: 2,
		Tags: []string{"go"}, Home: &Address{Street: "Main", Zip: "12345"},
		Labels: map[string]string{"a": "b"}, Code: "ABC",
	}
}

func fieldErrors(t *testing.T, err error) map[string]string {
	t.Helper()
	var errs validate.Errors
	if !errors.As(err, &errs) {
		t.Fatalf("expected validate.Errors, got %v", err)
	}
	out := map[string]string{}
	for _, e := range errs {
		out[e.Field] = e.Rule
	}
	return out
}

func TestValidStruct(t *testing.T) {
	a := valid()
	if err := validate.Struct(&a); err != nil {
		t.Fatal(err)
	}
	if err := validate.Struct(a); err != nil {
		t.Fatal("values and pointers must validate the same")
	}
}

func TestInvalidStruct(t *testing.T) {
	nick := "x"
	a := Account{
		Name: "A", Email: "not-an-email", Age: 10, Score: 1, Role: "root", Level: 7,
		Tags: []string{"ok", "bad tag", "toolong"}, Home: &Address{Zip: "12a45"},
		Others: []Address{{Street: "x", Zip: "12345"}, {}}, Labels: map[string]string{"a": "", "b": "x", "c": "y"},
		Website: "/relative", ID: "123", Timeout: time.Hour, Code: "abc", Nickname: &nick,
	}
	got := fieldErrors(t, validate.Struct(&a))
	want := map[string]string{
		"name": "min", "email": "email", "age": "gte", "score": "lt", "role": "oneof", "level": "oneof",
		"tags[1]": "alphanum", "tags[2]": "max", "home.street": "required", "home.zip": "numeric",
		"others[1].street": "required", "others[1].zip": "len", "labels": "max",
		"website": "url", "id": "uuid", "timeout": "max", "code": "pattern", "nickname": "min",
	}
	if !reflect.DeepEqual(got, want) {
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: want %s, got %q", k, v, got[k])
			}
		}
		for k, v := range got {
			if _, ok := want[k]; !ok {
				t.Errorf("unexpected %s: %s", k, v)
			}
		}
	}
}

func TestMessages(t *testing.T) {
	type M struct {
		S []int  `json:"s" validate:"min=2"`
		N string `json:"n" validate:"required"`
		O string `json:"o" validate:"oneof=a b"`
	}
	err := validate.Struct(M{S: []int{1}, O: "c"})
	msg := err.Error()
	for _, want := range []string{"s: must contain at least 2 items", "n: is required", "o: must be one of: a, b"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in %q", want, msg)
		}
	}
}

type Range struct {
	From int `json:"from"`
	To   int `json:"to"`
}

func (r Range) Validate() error {
	if r.To < r.From {
		return validate.FieldError{Field: "to", Rule: "gtefield", Message: "must not be before from"}
	}
	return nil
}

type Booking struct {
	Range Range `json:"range"`
}

func TestValidatable(t *testing.T) {
	got := fieldErrors(t, validate.Struct(&Booking{Range: Range{From: 5, To: 1}}))
	if got["range.to"] != "gtefield" {
		t.Fatalf("got %v", got)
	}
}

func TestCustomRule(t *testing.T) {
	v := validate.New()
	if err := v.RegisterRule("even", func(rv reflect.Value, _ string) bool { return rv.Int()%2 == 0 }, "must be even"); err != nil {
		t.Fatal(err)
	}
	type E struct {
		N int `json:"n" validate:"even"`
	}
	if got := fieldErrors(t, v.Struct(E{N: 3})); got["n"] != "even" {
		t.Fatalf("got %v", got)
	}
	if err := v.RegisterRule("required", func(reflect.Value, string) bool { return true }, ""); err == nil {
		t.Fatal("reserved rule names must be rejected")
	}
}

func TestInvalidTagsAreReported(t *testing.T) {
	type Bad struct {
		A string `validate:"min=x"`
	}
	type Unknown struct {
		A string `validate:"nope"`
	}
	type BadKind struct {
		A int `validate:"email"`
	}
	for _, v := range []any{Bad{}, Unknown{}, BadKind{}} {
		err := validate.Struct(v)
		var errs validate.Errors
		if err == nil || errors.As(err, &errs) {
			t.Errorf("%T: expected a tag error, got %v", v, err)
		}
		if validate.Default.Compile(reflect.TypeOf(v)) == nil {
			t.Errorf("%T: Compile must report the tag error", v)
		}
	}
}

type Node struct {
	Name     string `json:"name" validate:"required"`
	Children []Node `json:"children"`
}

func TestRecursiveTypes(t *testing.T) {
	n := Node{Name: "root", Children: []Node{{Name: "a"}, {Children: []Node{{}}}}}
	got := fieldErrors(t, validate.Struct(&n))
	if got["children[1].name"] != "required" || got["children[1].children[0].name"] != "required" {
		t.Fatalf("got %v", got)
	}
}

func BenchmarkValidate(b *testing.B) {
	a := valid()
	_ = validate.Struct(&a)
	b.ReportAllocs()
	for b.Loop() {
		_ = validate.Struct(&a)
	}
}
