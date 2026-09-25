package torge

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Offset pagination, cursor pagination, sorting and search helpers.
//
// The structs are designed to be embedded in handler inputs so binding,
// validation and OpenAPI documentation follow from a single declaration:
//
//	type ListUsers struct {
//		torge.PageParams
//		torge.Search
//		Role string `query:"role" validate:"omitempty,oneof=admin member"`
//	}
//
//	torge.Get(api, "/users", func(c *torge.Context, in *ListUsers) (*torge.Page[User], error) {
//		users, total := store.List(c.Context(), in.Offset(), in.Limit(), in.Q)
//		c.SetPageLinks("/api/v1/users", in.PageParams, total)
//		return torge.NewPage(users, total, in.PageParams), nil
//	})
//
// See docs/guide.md ("Binding and validation") for the tag rules.

// Pagination bounds shared by PageParams and CursorParams. The validate tags
// below mirror these values because struct tags cannot reference constants.
const (
	// DefaultPageSize is used when the client sends no page size.
	DefaultPageSize = 20
	// MaxPageSize caps the page size a client may request.
	MaxPageSize = 100
)

// PageParams declares offset-pagination query parameters. Embed it in a
// handler input; Context.Bind applies defaults and validates bounds.
type PageParams struct {
	Page     int `query:"page" default:"1" validate:"min=1"`
	PageSize int `query:"page_size" default:"20" validate:"min=1,max=100"`
}

// Offset returns the number of items to skip for the current page. Values
// below the valid range are clamped so direct construction cannot produce a
// negative offset.
func (p PageParams) Offset() int {
	page, size := p.Page, p.PageSize
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = DefaultPageSize
	}
	return (page - 1) * size
}

// Limit returns the maximum number of items to return for the current page.
func (p PageParams) Limit() int {
	if p.PageSize < 1 {
		return DefaultPageSize
	}
	if p.PageSize > MaxPageSize {
		return MaxPageSize
	}
	return p.PageSize
}

// Page is an offset-pagination response envelope. Total is the number of
// items across all pages; Pages is derived from Total and the page size.
type Page[T any] struct {
	Items    []T `json:"items"`
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Pages    int `json:"pages"`
}

// NewPage builds an envelope for items with total matching items across all
// pages. A nil slice is normalized to an empty array so clients always see
// an array. Non-positive totals yield zero pages.
func NewPage[T any](items []T, total int, params PageParams) *Page[T] {
	if items == nil {
		items = []T{}
	}
	size := params.Limit()
	pages := 0
	if total > 0 {
		pages = (total + size - 1) / size
	}
	page := params.Page
	if page < 1 {
		page = 1
	}
	return &Page[T]{Items: items, Total: total, Page: page, PageSize: size, Pages: pages}
}

// HasNext reports whether a page after the current one exists.
func (p *Page[T]) HasNext() bool { return p != nil && p.Page < p.Pages }

// HasPrev reports whether a page before the current one exists.
func (p *Page[T]) HasPrev() bool { return p != nil && p.Page > 1 }

// CursorParams declares cursor-pagination query parameters. The cursor itself
// is opaque to the framework; applications encode whatever resume point they
// need (an ID, timestamp or offset) and decode it in the handler.
type CursorParams struct {
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" default:"20" validate:"min=1,max=100"`
}

// CursorLimit returns the maximum number of items to return, clamped to the
// valid range so direct construction stays safe.
func (p CursorParams) CursorLimit() int {
	if p.Limit < 1 {
		return DefaultPageSize
	}
	if p.Limit > MaxPageSize {
		return MaxPageSize
	}
	return p.Limit
}

// CursorPage is a cursor-pagination response envelope. NextCursor is empty
// when there are no more items.
type CursorPage[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

// NewCursorPage builds an envelope. A nil slice is normalized to an empty
// array; HasMore is set exactly when nextCursor is non-empty.
func NewCursorPage[T any](items []T, nextCursor string) *CursorPage[T] {
	if items == nil {
		items = []T{}
	}
	return &CursorPage[T]{Items: items, NextCursor: nextCursor, HasMore: nextCursor != ""}
}

// Sort is a single validated sort clause: Field names a sortable field and
// Desc selects descending order.
type Sort struct {
	Field string
	Desc  bool
}

// ParseSort validates a comma-separated sort parameter against an allow-list
// of sortable fields. A leading "-" marks descending order, e.g.
// "-created_at,name". An empty parameter yields no clauses and no error.
// Unknown fields fail with a 400 *Error naming the allowed fields, ready to
// be returned from the handler.
func ParseSort(param string, allowed ...string) ([]Sort, error) {
	if strings.TrimSpace(param) == "" {
		return nil, nil
	}
	set := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		set[a] = struct{}{}
	}
	parts := strings.Split(param, ",")
	out := make([]Sort, 0, len(parts))
	for _, part := range parts {
		field := strings.TrimSpace(part)
		desc := false
		if strings.HasPrefix(field, "-") {
			desc = true
			field = strings.TrimSpace(strings.TrimPrefix(field, "-"))
		}
		if _, ok := set[field]; !ok || field == "" {
			return nil, BadRequest(CodeBadRequest,
				fmt.Sprintf("Unknown sort field %q; allowed: %s", strings.TrimSpace(part), strings.Join(allowed, ", ")))
		}
		out = append(out, Sort{Field: field, Desc: desc})
	}
	return out, nil
}

// Search declares a free-text query parameter. Embed it in a handler input
// alongside PageParams for searchable list endpoints.
type Search struct {
	Q string `query:"q" validate:"max=100"`
}

// SetPageLinks sets an RFC 8288 Link header for an offset-paginated resource
// at path with first/prev/next/last relations, preserving the page size.
// Clients that understand links can follow pages without building URLs;
// others can ignore the header. Call it before writing the response body.
func (c *Context) SetPageLinks(path string, params PageParams, total int) {
	size := params.Limit()
	page := params.Page
	if page < 1 {
		page = 1
	}
	pages := 0
	if total > 0 {
		pages = (total + size - 1) / size
	}
	last := pages
	if last < 1 {
		last = 1
	}
	link := func(p int, rel string) string {
		q := url.Values{}
		q.Set("page", strconv.Itoa(p))
		q.Set("page_size", strconv.Itoa(size))
		u := url.URL{Path: path, RawQuery: q.Encode()}
		return "<" + u.String() + `>; rel="` + rel + `"`
	}
	links := []string{link(1, "first"), link(last, "last")}
	if page > 1 {
		prev := page - 1
		if prev < 1 {
			prev = 1
		}
		links = append(links, link(prev, "prev"))
	}
	if page < last {
		links = append(links, link(page+1, "next"))
	}
	c.Header("Link", strings.Join(links, ", "))
}
