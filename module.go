package torge

import (
	"fmt"
	"reflect"
)

// Module groups the registrations of one area of an application: routes,
// middleware, providers, hooks, health checks, jobs and OpenAPI metadata.
// Modules do not impose an architecture; they are simply a named unit of
// registration.
//
//	type UserModule struct{}
//
//	func (UserModule) Register(app *torge.App) error {
//	    app.Provide(NewUserStore)
//	    app.Provide(NewUserService)
//	    app.Invoke(func(svc *UserService) {
//	        users := app.Group("/users", torge.Tags("users"))
//	        torge.Get(users, "/:id", svc.Get)
//	    })
//	    return nil
//	}
//
// Routes registered by a module (including inside its Invoke functions) are
// attributed to it in route introspection. Implement Name to control the
// module name; by default the type name is used.
type Module interface {
	Register(app *App) error
}

// NamedModule is a Module with an explicit name.
type NamedModule interface {
	Module
	Name() string
}

// ModuleFunc adapts a function to a Module.
type ModuleFunc func(app *App) error

// Register implements Module.
func (f ModuleFunc) Register(app *App) error { return f(app) }

// Register registers modules in order. Registering two modules with the same
// name is reported as a diagnostic.
func (a *App) Register(mods ...Module) {
	loc := callerLocation()
	for _, m := range mods {
		name := moduleName(m)
		a.mu.Lock()
		a.mustBeRegistering(loc, "module "+name)
		if a.modules[name] {
			a.addDiagnostic(&Diagnostic{
				Code: DiagDuplicateModule, What: fmt.Sprintf("module %q registered twice", name), Where: loc,
				Why: "its routes and providers would be registered twice", Fix: "register each module once",
			})
			a.mu.Unlock()
			continue
		}
		a.modules[name] = true
		prev := a.currentModule
		a.currentModule = name
		a.mu.Unlock()

		err := m.Register(a)

		a.mu.Lock()
		a.currentModule = prev
		if err != nil {
			a.addDiagnostic(&Diagnostic{
				Code: DiagModuleFailed, What: fmt.Sprintf("module %q failed to register: %v", name, err), Where: loc,
				Why: "the module's features would be missing", Fix: "fix the error returned by the module's Register method",
			})
		}
		a.mu.Unlock()
	}
}

// Modules returns the names of registered modules.
func (a *App) Modules() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.modules))
	for name := range a.modules {
		out = append(out, name)
	}
	return out
}

func moduleName(m Module) string {
	if n, ok := m.(NamedModule); ok {
		return n.Name()
	}
	if f, ok := m.(ModuleFunc); ok {
		return funcName(f)
	}
	t := reflect.TypeOf(m)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Name() == "" {
		return t.String()
	}
	return t.Name()
}
