package validate

import (
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type messageFunc func(param string, t reflect.Type) string

type builtinRule struct {
	factory ruleFactory
	message messageFunc
}

var durationType = reflect.TypeFor[time.Duration]()

var builtinRules = map[string]builtinRule{
	"min": {compareFactory(func(a, b float64) bool { return a >= b }), sizeMessage("at least")},
	"max": {compareFactory(func(a, b float64) bool { return a <= b }), sizeMessage("at most")},
	"len": {compareFactory(func(a, b float64) bool { return a == b }), sizeMessage("exactly")},
	"gt":  {compareFactory(func(a, b float64) bool { return a > b }), sizeMessage("greater than")},
	"gte": {compareFactory(func(a, b float64) bool { return a >= b }), sizeMessage("at least")},
	"lt":  {compareFactory(func(a, b float64) bool { return a < b }), sizeMessage("less than")},
	"lte": {compareFactory(func(a, b float64) bool { return a <= b }), sizeMessage("at most")},
	"eq":  {equalFactory(true), static("must be equal to %s")},
	"ne":  {equalFactory(false), static("must not be equal to %s")},

	"oneof": {oneofFactory, func(p string, _ reflect.Type) string {
		return "must be one of: " + strings.Join(strings.Fields(p), ", ")
	}},

	"email":      {stringRule(isEmail), static("must be a valid email address")},
	"url":        {stringRule(isURL), static("must be a valid absolute URL")},
	"uuid":       {stringRule(IsUUID), static("must be a valid UUID")},
	"alpha":      {stringRule(isAlpha), static("must contain only letters")},
	"alphanum":   {stringRule(isAlphaNum), static("must contain only letters and digits")},
	"numeric":    {stringRule(isNumeric), static("must be a numeric string")},
	"lowercase":  {stringRule(func(s string) bool { return s == strings.ToLower(s) }), static("must be lowercase")},
	"uppercase":  {stringRule(func(s string) bool { return s == strings.ToUpper(s) }), static("must be uppercase")},
	"ip":         {stringRule(func(s string) bool { return net.ParseIP(s) != nil }), static("must be a valid IP address")},
	"ipv4":       {stringRule(isIPv4), static("must be a valid IPv4 address")},
	"ipv6":       {stringRule(isIPv6), static("must be a valid IPv6 address")},
	"contains":   {stringParamRule(strings.Contains), static("must contain %s")},
	"excludes":   {stringParamRule(func(s, p string) bool { return !strings.Contains(s, p) }), static("must not contain %s")},
	"startswith": {stringParamRule(strings.HasPrefix), static("must start with %s")},
	"endswith":   {stringParamRule(strings.HasSuffix), static("must end with %s")},
}

func static(msg string) messageFunc {
	return func(param string, _ reflect.Type) string {
		return strings.ReplaceAll(msg, "%s", param)
	}
}

func sizeMessage(op string) messageFunc {
	return func(param string, t reflect.Type) string {
		switch t.Kind() {
		case reflect.String:
			return fmt.Sprintf("must be %s %s characters long", op, param)
		case reflect.Slice, reflect.Array, reflect.Map:
			return fmt.Sprintf("must contain %s %s items", op, param)
		default:
			return fmt.Sprintf("must be %s %s", op, param)
		}
	}
}

func parseNumber(param string, t reflect.Type) (float64, error) {
	if t == durationType {
		d, err := time.ParseDuration(param)
		if err != nil {
			return 0, fmt.Errorf("invalid duration parameter %q", param)
		}
		return float64(d), nil
	}
	n, err := strconv.ParseFloat(param, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid numeric parameter %q", param)
	}
	return n, nil
}

// measure returns a function extracting the comparable size of a value of
// kind k: rune count for strings, length for containers, value for numbers.
func measure(t reflect.Type) (func(reflect.Value) float64, error) {
	switch t.Kind() {
	case reflect.String:
		return func(v reflect.Value) float64 { return float64(utf8.RuneCountInString(v.String())) }, nil
	case reflect.Slice, reflect.Array, reflect.Map:
		return func(v reflect.Value) float64 { return float64(v.Len()) }, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return func(v reflect.Value) float64 { return float64(v.Int()) }, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return func(v reflect.Value) float64 { return float64(v.Uint()) }, nil
	case reflect.Float32, reflect.Float64:
		return func(v reflect.Value) float64 { return v.Float() }, nil
	default:
		return nil, fmt.Errorf("unsupported type %s", t)
	}
}

func compareFactory(cmp func(a, b float64) bool) ruleFactory {
	return func(param string, t reflect.Type) (checkFunc, error) {
		n, err := parseNumber(param, t)
		if err != nil {
			return nil, err
		}
		m, err := measure(t)
		if err != nil {
			return nil, err
		}
		return func(v reflect.Value) bool { return cmp(m(v), n) }, nil
	}
}

func equalFactory(want bool) ruleFactory {
	return func(param string, t reflect.Type) (checkFunc, error) {
		if t.Kind() == reflect.String {
			return func(v reflect.Value) bool { return (v.String() == param) == want }, nil
		}
		if t.Kind() == reflect.Bool {
			b, err := strconv.ParseBool(param)
			if err != nil {
				return nil, fmt.Errorf("invalid boolean parameter %q", param)
			}
			return func(v reflect.Value) bool { return (v.Bool() == b) == want }, nil
		}
		cmp, err := compareFactory(func(a, b float64) bool { return a == b })(param, t)
		if err != nil {
			return nil, err
		}
		return func(v reflect.Value) bool { return cmp(v) == want }, nil
	}
}

func oneofFactory(param string, t reflect.Type) (checkFunc, error) {
	options := strings.Fields(param)
	if len(options) == 0 {
		return nil, fmt.Errorf("oneof requires at least one option")
	}
	switch t.Kind() {
	case reflect.String:
		set := make(map[string]struct{}, len(options))
		for _, o := range options {
			set[o] = struct{}{}
		}
		return func(v reflect.Value) bool { _, ok := set[v.String()]; return ok }, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		set := make(map[int64]struct{}, len(options))
		for _, o := range options {
			n, err := strconv.ParseInt(o, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid integer option %q", o)
			}
			set[n] = struct{}{}
		}
		return func(v reflect.Value) bool { _, ok := set[v.Int()]; return ok }, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		set := make(map[uint64]struct{}, len(options))
		for _, o := range options {
			n, err := strconv.ParseUint(o, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid unsigned option %q", o)
			}
			set[n] = struct{}{}
		}
		return func(v reflect.Value) bool { _, ok := set[v.Uint()]; return ok }, nil
	default:
		return nil, fmt.Errorf("unsupported type %s", t)
	}
}

func stringRule(fn func(string) bool) ruleFactory {
	return func(_ string, t reflect.Type) (checkFunc, error) {
		if t.Kind() != reflect.String {
			return nil, fmt.Errorf("requires a string, got %s", t)
		}
		return func(v reflect.Value) bool { return fn(v.String()) }, nil
	}
}

func stringParamRule(fn func(s, param string) bool) ruleFactory {
	return func(param string, t reflect.Type) (checkFunc, error) {
		if t.Kind() != reflect.String {
			return nil, fmt.Errorf("requires a string, got %s", t)
		}
		return func(v reflect.Value) bool { return fn(v.String(), param) }, nil
	}
}

func isEmail(s string) bool {
	if len(s) > 254 || strings.ContainsAny(s, " <>") {
		return false
	}
	if valid, decided := simpleEmail(s); decided {
		return valid
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s {
		return false
	}
	at := strings.LastIndexByte(s, '@')
	return at > 0 && strings.Contains(s[at+1:], ".")
}

// atextBytes marks the RFC 5322 atext characters (letters, digits and
// !#$%&'*+-/=?^_`{|}~).
var atextBytes = func() (t [256]bool) {
	for c := range 256 {
		b := byte(c)
		t[c] = 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || '0' <= b && b <= '9' ||
			strings.IndexByte("!#$%&'*+-/=?^_`{|}~", b) >= 0
	}
	return t
}()

// simpleEmail decides, without allocating, the addresses whose result is
// certain: plain "local@domain" dot-atoms. It reports decided=false for
// anything else (quotes, comments, non-ASCII, unusual dots), which is then
// checked with net/mail exactly as before.
func simpleEmail(s string) (valid, decided bool) {
	at := -1
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '@':
			if at >= 0 {
				return false, true // several @ in plain text never parse
			}
			at = i
		case c != '.' && !atextBytes[c]:
			return false, false
		}
	}
	if at < 0 {
		return false, true // plain text without @ never parses
	}
	local, domain := s[:at], s[at+1:]
	if !cleanDotAtom(local) || !cleanDotAtom(domain) {
		return false, false
	}
	return strings.IndexByte(domain, '.') >= 0, true
}

// cleanDotAtom reports whether s is non-empty with no leading, trailing or
// doubled dots.
func cleanDotAtom(s string) bool {
	if s == "" || s[0] == '.' || s[len(s)-1] == '.' {
		return false
	}
	return !strings.Contains(s, "..")
}

func isURL(s string) bool {
	u, err := url.ParseRequestURI(s)
	return err == nil && u.Scheme != "" && u.Host != ""
}

// IsUUID reports whether s is a canonical, hyphenated UUID.
func IsUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !isHex(c) {
				return false
			}
		}
	}
	return true
}

func isHex(c byte) bool {
	return ('0' <= c && c <= '9') || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F')
}

func isAlpha(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

func isAlphaNum(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

func isIPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil && !strings.Contains(s, ":")
}

func isIPv6(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && strings.Contains(s, ":")
}
