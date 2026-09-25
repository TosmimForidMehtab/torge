package torge

// Result is a uniform success envelope for single-resource responses. List
// responses use Page[T]; this covers the "one object plus metadata" case:
//
//	torge.Get(api, "/users/:id", func(c *torge.Context, in *GetUser) (*torge.Result[User], error) {
//		u, err := users.Find(c.Context(), in.ID)
//		if err != nil {
//			return nil, err
//		}
//		return torge.NewResult(u).WithMeta("cache", "hit"), nil
//	})
//
// The shape renders as {"data": ..., "meta": {...}} with meta omitted when
// empty, so clients that only need the resource can ignore the envelope.
type Result[T any] struct {
	Data T              `json:"data"`
	Meta map[string]any `json:"meta,omitempty"`
}

// NewResult wraps data in a success envelope.
func NewResult[T any](data T) *Result[T] { return &Result[T]{Data: data} }

// WithMeta attaches a metadata entry, allocating the map on first use. It
// returns the receiver for chaining.
func (r *Result[T]) WithMeta(key string, value any) *Result[T] {
	if r.Meta == nil {
		r.Meta = make(map[string]any)
	}
	r.Meta[key] = value
	return r
}
