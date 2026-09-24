package torge

import (
	"net/http"
	"reflect"

	"github.com/TosmimForidMehtab/torge/internal/binding"
)

// TypedHandler is a handler with a typed input and output. The input is bound
// and validated before the handler runs (see Context.Bind); the output is
// encoded with the configured success status. Returning a nil output writes
// 204 No Content.
//
//	type GetUserInput struct {
//	    ID string `path:"id" validate:"uuid"`
//	}
//
//	torge.Get(app, "/users/:id", func(c *torge.Context, in *GetUserInput) (*User, error) {
//	    return users.Find(c.Context(), in.ID)
//	}, torge.Summary("Get a user"))
//
// The same types drive the generated OpenAPI operation, so the contract is
// declared exactly once.
type TypedHandler[I, O any] func(c *Context, in *I) (*O, error)

// Empty is a convenience input or output type for operations without one.
type Empty struct{}

// Handle registers a typed handler for method and path on r.
func Handle[I, O any](r Router, method, path string, h TypedHandler[I, O], opts ...RouteOption) {
	inType, outType := reflect.TypeFor[I](), reflect.TypeFor[O]()
	var plan *binding.Plan
	var planErr error
	if inType.Kind() == reflect.Struct {
		plan, planErr = binding.PlanFor(inType)
	}
	cfg := &typedConfig{}
	handler := func(c *Context) error {
		if planErr != nil {
			return planErr
		}
		in := new(I)
		if plan != nil {
			if err := c.bindPlan(reflect.ValueOf(in).Elem(), plan); err != nil {
				return err
			}
			if err := c.Validate(in); err != nil {
				return err
			}
		}
		out, err := h(c, in)
		if err != nil {
			return err
		}
		if c.res.Written() {
			return nil
		}
		if out == nil || outType == emptyType {
			return c.NoContent(http.StatusNoContent)
		}
		status := cfg.status
		if status == 0 {
			status = http.StatusOK
		}
		return c.JSON(status, out)
	}
	all := make([]RouteOption, 0, len(opts)+1)
	all = append(all, typedOption{in: inType, out: outType, planErr: planErr, name: funcName(h)})
	all = append(all, opts...)
	// Capture the resolved success status after all options have applied.
	all = append(all, routeOptionFunc(func(rc *routeConfig) { cfg.status = rc.doc.successStatus }))
	if g, ok := r.(interface {
		handleAt(method, path string, h Handler, opts []RouteOption, loc string)
	}); ok {
		g.handleAt(method, path, handler, all, callerLocation())
		return
	}
	r.Handle(method, path, handler, all...)
}

type typedConfig struct{ status int }

var emptyType = reflect.TypeFor[Empty]()

type typedOption struct {
	in, out reflect.Type
	planErr error
	name    string
}

func (o typedOption) applyRoute(r *routeConfig) {
	r.doc.input = o.in
	if o.out != emptyType {
		if r.doc.responses == nil {
			r.doc.responses = make(map[int]responseDoc)
		}
		r.doc.responses[0] = responseDoc{typ: o.out}
	}
}

// Get registers a typed GET handler.
func Get[I, O any](r Router, path string, h TypedHandler[I, O], opts ...RouteOption) {
	Handle(r, http.MethodGet, path, h, opts...)
}

// Post registers a typed POST handler.
func Post[I, O any](r Router, path string, h TypedHandler[I, O], opts ...RouteOption) {
	Handle(r, http.MethodPost, path, h, opts...)
}

// Put registers a typed PUT handler.
func Put[I, O any](r Router, path string, h TypedHandler[I, O], opts ...RouteOption) {
	Handle(r, http.MethodPut, path, h, opts...)
}

// Patch registers a typed PATCH handler.
func Patch[I, O any](r Router, path string, h TypedHandler[I, O], opts ...RouteOption) {
	Handle(r, http.MethodPatch, path, h, opts...)
}

// Delete registers a typed DELETE handler.
func Delete[I, O any](r Router, path string, h TypedHandler[I, O], opts ...RouteOption) {
	Handle(r, http.MethodDelete, path, h, opts...)
}

// handleAt registers with an explicit caller location (used by typed helpers
// so diagnostics point at user code).
func (g *Group) handleAt(method, path string, h Handler, opts []RouteOption, loc string) {
	g.app.addRoute(g, method, path, h, opts, loc)
}
