package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIdentifiers(t *testing.T) {
	for in, want := range map[string]string{"blog-posts": "BlogPosts", "users": "Users", "x_y z": "XYZ", "9lives": "Lives"} {
		if got := identifier(in); got != want {
			t.Errorf("identifier(%q) = %q, want %q", in, got, want)
		}
	}
	if singular("Categories") != "Category" || singular("Users") != "User" || singular("Address") != "Address" {
		t.Fatal("singular")
	}
}

// TestScaffoldBuildsAndPasses generates a project against this checkout and
// runs its tests, proving the templates compile and work end to end.
func TestScaffoldBuildsAndPasses(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a generated project")
	}
	_, file, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(file), "..", "..")
	dir := t.TempDir()
	t.Chdir(dir)

	var out bytes.Buffer
	if err := run([]string{"new", "demo", "-module", "example.com/demo", "-local", repoRoot}, &out, &out); err != nil {
		t.Fatalf("new: %v\n%s", err, out.String())
	}
	t.Chdir(filepath.Join(dir, "demo"))
	for _, f := range []string{".env", ".env.example"} {
		b, err := os.ReadFile(f)
		if err != nil || !strings.Contains(string(b), "TORGE_ENV=development") {
			t.Fatalf("%s: %v\n%s", f, err, b)
		}
	}
	if b, _ := os.ReadFile(".gitignore"); !strings.Contains(string(b), ".env\n") {
		t.Fatal(".env must be git-ignored")
	}
	if b, _ := os.ReadFile(".dockerignore"); !strings.HasPrefix(string(b), ".env\n") {
		t.Fatal(".env must never be copied into container images")
	}
	if b, _ := os.ReadFile("Dockerfile"); !strings.Contains(string(b), "nonroot") {
		t.Fatal("the Dockerfile must run as a non-root user")
	}
	if err := run([]string{"generate", "resource", "blog-posts"}, &out, &out); err != nil {
		t.Fatalf("generate resource: %v", err)
	}
	if err := run([]string{"generate", "module", "billing"}, &out, &out); err != nil {
		t.Fatalf("generate module: %v", err)
	}
	if err := run([]string{"generate", "middleware", "audit"}, &out, &out); err != nil {
		t.Fatalf("generate middleware: %v", err)
	}
	if err := run([]string{"generate", "module", "billing"}, &out, &out); err == nil {
		t.Fatal("generators must not overwrite files")
	}
	for _, args := range [][]string{{"mod", "tidy"}, {"vet", "./..."}, {"test", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, b)
		}
	}
	var routes bytes.Buffer
	if err := run([]string{"routes"}, &routes, &routes); err != nil {
		t.Fatalf("routes: %v\n%s", err, routes.String())
	}
	if !strings.Contains(routes.String(), "/users/:id") || !strings.Contains(routes.String(), "users") {
		t.Fatalf("routes output:\n%s", routes.String())
	}
}
