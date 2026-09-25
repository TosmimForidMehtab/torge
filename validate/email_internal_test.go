package validate

import (
	"math/rand/v2"
	"net/mail"
	"strings"
	"testing"
)

// oldIsEmail is the original net/mail-only implementation, kept as an oracle.
func oldIsEmail(s string) bool {
	if len(s) > 254 || strings.ContainsAny(s, " <>") {
		return false
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s {
		return false
	}
	at := strings.LastIndexByte(s, '@')
	return at > 0 && strings.Contains(s[at+1:], ".")
}

func TestIsEmailAgreesWithNetMail(t *testing.T) {
	corpus := []string{
		"ada@example.com", "a.b.c@d.e.f", "first+tag@sub.example.co.uk", "x@y.z", "user_name-1@ex-ample.com",
		"!#$%&'*+-/=?^_`{|}~@example.com", "a@b", "a@b.", "a@.b", "a@b..c", ".a@b.c", "a.@b.c", "a..b@c.d",
		"@b.c", "a@", "a@@b.c", "a@b@c.d", "nope", "", ".", "@", "a@b.c.", "\"quoted\"@example.com",
		"\"john doe\"@example.com", "a(comment)@b.c", "a@[127.0.0.1]", "ünï@example.com", "a@exämple.com",
		"Name <a@b.c>", "a b@c.d", "a@b.c ", "a\\b@c.d", "a@b.c\x00", "a,b@c.d", "a;b@c.d", "a@b_c.d",
		strings.Repeat("a", 64) + "@example.com", strings.Repeat("a", 250) + "@b.c",
	}
	for _, s := range corpus {
		if got, want := isEmail(s), oldIsEmail(s); got != want {
			t.Errorf("isEmail(%q) = %v, original = %v", s, got, want)
		}
	}

	// Randomized inputs over an alphabet covering every branch of the fast path.
	alphabet := []string{"a", "Z", "0", ".", "@", "-", "_", "+", "!", "\"", "(", ")", "[", "]", "\\", " ", "é", ",", "~"}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 200000 {
		var b strings.Builder
		for range rng.IntN(12) {
			b.WriteString(alphabet[rng.IntN(len(alphabet))])
		}
		s := b.String()
		if got, want := isEmail(s), oldIsEmail(s); got != want {
			t.Fatalf("isEmail(%q) = %v, original = %v", s, got, want)
		}
	}
}

func BenchmarkIsEmail(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		isEmail("ada.lovelace+work@example.com")
	}
}
