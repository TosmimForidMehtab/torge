package torge

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func mustInsert(t *testing.T, rt *router, method, path string) *route {
	t.Helper()
	r := &route{method: method, path: path}
	existing, err := rt.insert(r)
	if err != nil || existing != nil {
		t.Fatalf("insert %s %s: err=%v existing=%v", method, path, err, existing)
	}
	return r
}

func TestRouterMatching(t *testing.T) {
	rt := newRouter()
	routes := map[string]*route{}
	for _, p := range []string{
		"/", "/users", "/users/me", "/users/:id", "/users/:userID/posts",
		"/users/:id/posts/:postID", "/static/*filepath", "/files/*", "/a/b/c", "/a/:x/d",
	} {
		routes[p] = mustInsert(t, rt, "GET", p)
	}
	tests := []struct {
		path    string
		pattern string
		params  map[string]string
	}{
		{"/", "/", nil},
		{"/users", "/users", nil},
		{"/users/me", "/users/me", nil},
		{"/users/42", "/users/:id", map[string]string{"id": "42"}},
		{"/users/42/posts", "/users/:userID/posts", map[string]string{"userID": "42"}},
		{"/users/42/posts/7", "/users/:id/posts/:postID", map[string]string{"id": "42", "postID": "7"}},
		{"/static/css/site.css", "/static/*filepath", map[string]string{"filepath": "css/site.css"}},
		{"/static/", "/static/*filepath", map[string]string{"filepath": ""}},
		{"/files/x/y", "/files/*", map[string]string{"*": "x/y"}},
		{"/a/b/c", "/a/b/c", nil},
		// Backtracking: static "b" fails for "/a/b/d", the parameter matches.
		{"/a/b/d", "/a/:x/d", map[string]string{"x": "b"}},
		{"/nope", "", nil},
		{"/users/42/unknown", "", nil},
		{"/users/", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			r, vals := rt.find("GET", tt.path, nil)
			if tt.pattern == "" {
				if r != nil {
					t.Fatalf("expected no match, got %s", r.path)
				}
				return
			}
			if r == nil {
				t.Fatalf("expected %s, got no match", tt.pattern)
			}
			if r != routes[tt.pattern] {
				t.Fatalf("expected %s, got %s", tt.pattern, r.path)
			}
			got := map[string]string{}
			for i, n := range r.paramNames {
				got[n] = vals[i]
			}
			if len(tt.params) == 0 && len(got) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.params) {
				t.Fatalf("params: expected %v, got %v", tt.params, got)
			}
		})
	}
}

func TestRouterRejectsInvalidPatterns(t *testing.T) {
	for _, p := range []string{"users", "/a//b", "/:", "/*x/y", "/a:b", "/:id/:id", "/x*"} {
		if _, err := newRouter().insert(&route{method: "GET", path: p}); err == nil {
			t.Errorf("expected %q to be rejected", p)
		}
	}
}

func TestRouterDetectsDuplicates(t *testing.T) {
	rt := newRouter()
	mustInsert(t, rt, "GET", "/users/:id")
	existing, err := rt.insert(&route{method: "GET", path: "/users/:userID"})
	if err != nil || existing == nil {
		t.Fatalf("expected a conflict with the first route, err=%v", err)
	}
	// Same path with another method is fine.
	mustInsert(t, rt, "POST", "/users/:id")
}

func TestRouterAllowed(t *testing.T) {
	rt := newRouter()
	mustInsert(t, rt, "GET", "/users/:id")
	mustInsert(t, rt, "DELETE", "/users/:id")
	got := strings.Join(rt.allowed("/users/1"), ",")
	if got != "DELETE,GET,HEAD,OPTIONS" {
		t.Fatalf("unexpected allowed methods %q", got)
	}
	if rt.allowed("/missing") != nil {
		t.Fatal("expected no methods for an unknown path")
	}
}

func TestBuildURL(t *testing.T) {
	r := &route{path: "/users/:id/files/*path"}
	got, err := r.buildURL([]string{"id", "a b", "path", "x/y z.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "/users/a%20b/files/x/y%20z.txt" {
		t.Fatalf("got %q", got)
	}
	if _, err := r.buildURL([]string{"id", "1"}); err == nil {
		t.Fatal("expected missing parameter error")
	}
}

func TestFuncName(t *testing.T) {
	if got := funcName(Recovery()); got != "torge.Recovery" {
		t.Fatalf("got %q", got)
	}
	if got := funcName(TestFuncName); got != "torge.TestFuncName" {
		t.Fatalf("got %q", got)
	}
}

func TestRouterWideNodes(t *testing.T) {
	rt := newRouter()
	routes := map[string]*route{}
	for i := range 20 {
		p := fmt.Sprintf("/r%d/x", i)
		routes[p] = mustInsert(t, rt, "GET", p)
	}
	mustInsert(t, rt, "GET", "/:any/x")
	for p, want := range routes {
		if r, _ := rt.find("GET", p, nil); r != want {
			t.Fatalf("%s: wrong route", p)
		}
	}
	if r, vals := rt.find("GET", "/other/x", nil); r == nil || vals[0] != "other" {
		t.Fatal("parameter fallback on a wide node failed")
	}
}
