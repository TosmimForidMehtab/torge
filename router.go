package torge

import (
	"fmt"
	"sort"
	"strings"
)

// The router is a segment trie per HTTP method. Each node has static children
// keyed by segment, at most one parameter child and at most one wildcard
// child. Matching prefers static over parameter over wildcard segments and
// backtracks, so "/users/me" and "/users/:id" coexist predictably.
//
// Parameter names live on routes rather than nodes, which lets
// "/users/:id" and "/users/:userID/posts" share a node.

type node struct {
	// Static children: a linear scan over labels is faster than hashing for
	// the few children most nodes have; index is built only for wide nodes.
	labels   []string
	kids     []*node
	index    map[string]*node
	param    *node
	wildcard *node
	route    *route
}

// maxLinearChildren is the fan-out above which static children are indexed
// by a map.
const maxLinearChildren = 8

func (n *node) staticChild(seg string) *node {
	if n.index != nil {
		return n.index[seg]
	}
	for i, l := range n.labels {
		if l == seg {
			return n.kids[i]
		}
	}
	return nil
}

func (n *node) addStatic(seg string) *node {
	if child := n.staticChild(seg); child != nil {
		return child
	}
	child := &node{}
	n.labels = append(n.labels, seg)
	n.kids = append(n.kids, child)
	if len(n.labels) > maxLinearChildren {
		if n.index == nil {
			n.index = make(map[string]*node, len(n.labels))
			for i, l := range n.labels {
				n.index[l] = n.kids[i]
			}
		}
		n.index[seg] = child
	}
	return child
}

type router struct {
	trees     map[string]*node
	maxParams int
}

func newRouter() *router {
	return &router{trees: make(map[string]*node)}
}

// parsePattern validates a route pattern and returns its parameter names.
func parsePattern(pattern string) ([]string, error) {
	if pattern == "" || pattern[0] != '/' {
		return nil, fmt.Errorf("path %q must begin with '/'", pattern)
	}
	if pattern == "/" {
		return nil, nil
	}
	segs := strings.Split(pattern[1:], "/")
	var names []string
	seen := make(map[string]bool)
	for i, seg := range segs {
		if seg == "" {
			if i != len(segs)-1 {
				return nil, fmt.Errorf("path %q contains an empty segment", pattern)
			}
			continue
		}
		switch seg[0] {
		case ':', '*':
			name := seg[1:]
			if seg[0] == ':' && name == "" {
				return nil, fmt.Errorf("path %q has a parameter without a name", pattern)
			}
			if seg[0] == '*' && i != len(segs)-1 {
				return nil, fmt.Errorf("path %q: wildcard %q must be the last segment", pattern, seg)
			}
			if strings.ContainsAny(name, ":*") {
				return nil, fmt.Errorf("path %q has an invalid parameter %q", pattern, seg)
			}
			if name == "" {
				name = "*"
			}
			if seen[name] {
				return nil, fmt.Errorf("path %q repeats parameter %q", pattern, name)
			}
			seen[name] = true
			names = append(names, name)
		default:
			if strings.ContainsAny(seg, ":*") {
				return nil, fmt.Errorf("path %q: segment %q mixes literal text and parameters; parameters must span a whole segment", pattern, seg)
			}
		}
	}
	return names, nil
}

// insert adds r to the tree for its method. It returns the conflicting route
// when a route with the same method and shape already exists.
func (rt *router) insert(r *route) (*route, error) {
	names, err := parsePattern(r.path)
	if err != nil {
		return nil, err
	}
	r.paramNames = names
	if len(names) > rt.maxParams {
		rt.maxParams = len(names)
	}
	root := rt.trees[r.method]
	if root == nil {
		root = &node{}
		rt.trees[r.method] = root
	}
	n := root
	if r.path != "/" {
		for seg := range strings.SplitSeq(r.path[1:], "/") {
			switch {
			case seg != "" && seg[0] == ':':
				if n.param == nil {
					n.param = &node{}
				}
				n = n.param
			case seg != "" && seg[0] == '*':
				if n.wildcard == nil {
					n.wildcard = &node{}
				}
				n = n.wildcard
			default:
				n = n.addStatic(seg)
			}
		}
	}
	if n.route != nil {
		return n.route, nil
	}
	n.route = r
	return nil, nil
}

// find matches method and path. values receives parameter values in pattern
// order; it is returned (possibly grown) for reuse.
func (rt *router) find(method, path string, values []string) (*route, []string) {
	root := rt.trees[method]
	if root == nil {
		return nil, values
	}
	return root.lookup(path, values)
}

func (n *node) lookup(path string, values []string) (*route, []string) {
	if path == "" || path == "/" {
		if n.route != nil {
			return n.route, values
		}
		if n.wildcard != nil && n.wildcard.route != nil {
			return n.wildcard.route, append(values, "")
		}
		return nil, values
	}
	if path[0] != '/' {
		return nil, values
	}
	return n.match(path[1:], values)
}

// match matches rem, the remainder of the path after a '/', against n's
// children.
func (n *node) match(rem string, values []string) (*route, []string) {
	seg, next, more := strings.Cut(rem, "/")
	if child := n.staticChild(seg); child != nil {
		if r, v := child.descend(next, more, values); r != nil {
			return r, v
		}
	}
	if n.param != nil && seg != "" {
		if r, v := n.param.descend(next, more, append(values, seg)); r != nil {
			return r, v
		}
	}
	if n.wildcard != nil && n.wildcard.route != nil {
		return n.wildcard.route, append(values, rem)
	}
	return nil, values
}

func (n *node) descend(next string, more bool, values []string) (*route, []string) {
	if !more {
		return n.route, values
	}
	return n.match(next, values)
}

// allowed returns the sorted methods that match path, used for 405 responses
// and automatic OPTIONS.
func (rt *router) allowed(path string) []string {
	var methods []string
	buf := make([]string, 0, rt.maxParams)
	for method, root := range rt.trees {
		if r, _ := root.lookup(path, buf[:0]); r != nil {
			methods = append(methods, method)
		}
	}
	if len(methods) == 0 {
		return nil
	}
	if contains(methods, "GET") && !contains(methods, "HEAD") {
		methods = append(methods, "HEAD")
	}
	if !contains(methods, "OPTIONS") {
		methods = append(methods, "OPTIONS")
	}
	sort.Strings(methods)
	return methods
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
