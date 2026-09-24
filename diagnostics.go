package torge

import (
	"fmt"
	"runtime"
	"strings"
)

// Diagnostic is an actionable description of a setup or runtime problem. It
// explains what happened, where, why it matters, and how to fix it.
type Diagnostic struct {
	// Code identifies the kind of problem, for example "DUPLICATE_ROUTE".
	Code string
	// What happened.
	What string
	// Where it happened (usually file:line of the offending registration).
	Where string
	// Why it matters.
	Why string
	// Fix explains how to resolve it.
	Fix string
	// Warning marks diagnostics that are reported but do not stop startup.
	Warning bool
}

// Diagnostic codes.
const (
	DiagDuplicateRoute       = "DUPLICATE_ROUTE"
	DiagInvalidRoute         = "INVALID_ROUTE"
	DiagDuplicateRouteName   = "DUPLICATE_ROUTE_NAME"
	DiagLateRegistration     = "REGISTRATION_AFTER_START"
	DiagInvalidState         = "INVALID_LIFECYCLE_STATE"
	DiagMissingDependency    = "MISSING_DEPENDENCY"
	DiagDuplicateDependency  = "DUPLICATE_DEPENDENCY"
	DiagDependencyCycle      = "DEPENDENCY_CYCLE"
	DiagInvalidScope         = "INVALID_SCOPE"
	DiagInvalidProvider      = "INVALID_PROVIDER"
	DiagDuplicateService     = "DUPLICATE_SERVICE"
	DiagDuplicateModule      = "DUPLICATE_MODULE"
	DiagModuleFailed         = "MODULE_REGISTRATION_FAILED"
	DiagInvalidConfig        = "INVALID_CONFIGURATION"
	DiagUnsafeProduction     = "UNSAFE_PRODUCTION_CONFIGURATION"
	DiagDatabaseUnreachable  = "DATABASE_UNREACHABLE"
	DiagStartFailed          = "START_HOOK_FAILED"
	DiagInvalidHandler       = "INVALID_HANDLER"
	DiagDuplicateHealthCheck = "DUPLICATE_HEALTH_CHECK"
	DiagLocalState           = "PROCESS_LOCAL_STATE"
	DiagInvokeFailed         = "INVOKE_FAILED"
)

func (d *Diagnostic) Error() string {
	var b strings.Builder
	b.WriteString("torge: ")
	b.WriteString(d.Code)
	b.WriteString(": ")
	b.WriteString(d.What)
	if d.Where != "" {
		b.WriteString("\n    where: ")
		b.WriteString(d.Where)
	}
	if d.Why != "" {
		b.WriteString("\n    why:   ")
		b.WriteString(d.Why)
	}
	if d.Fix != "" {
		b.WriteString("\n    fix:   ")
		b.WriteString(d.Fix)
	}
	return b.String()
}

// Diagnostics is a list of problems. It implements error so that startup can
// report every problem at once.
type Diagnostics []*Diagnostic

func (ds Diagnostics) Error() string {
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = d.Error()
	}
	return fmt.Sprintf("torge: %d problem(s) prevent startup:\n%s", len(ds), strings.Join(parts, "\n"))
}

// Unwrap exposes the individual diagnostics to errors.Is and errors.As.
func (ds Diagnostics) Unwrap() []error {
	errs := make([]error, len(ds))
	for i, d := range ds {
		errs[i] = d
	}
	return errs
}

// callerLocation returns "file:line" of the first caller outside this package,
// used to tell developers where a problematic registration happened.
func callerLocation() string {
	pcs := make([]uintptr, 16)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if !isFrameworkFrame(f.Function) || strings.HasSuffix(f.File, "_test.go") {
			return fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		if !more {
			return ""
		}
	}
}

func isFrameworkFrame(fn string) bool {
	const pkg = "github.com/TosmimForidMehtab/torge."
	return strings.HasPrefix(fn, pkg)
}
