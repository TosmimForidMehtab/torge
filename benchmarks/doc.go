// Package benchmarks compares Torge with net/http, Gin and Echo on identical
// routes and handlers. It is a separate module so the framework itself never
// depends on other frameworks.
//
//	cd benchmarks && go test -bench . -benchmem
//
// Fiber is omitted: it is built on fasthttp rather than net/http, so an
// in-process comparison through http.Handler would not be like-for-like.
package benchmarks
