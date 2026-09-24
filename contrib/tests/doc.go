// Package tests holds the contrib modules' tests that need test-only
// dependencies (the OpenTelemetry SDK, miniredis). Keeping them in their own
// module keeps those dependencies out of the contrib modules' go.mod files,
// so applications using a contrib module never download or resolve them.
package tests
