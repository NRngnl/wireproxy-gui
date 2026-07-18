package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/NRngnl/wireproxy-gui"

func TestInternalDependenciesPointInward(t *testing.T) {
	root := repositoryRoot(t)
	rules := map[string][]string{
		"application": {"connection", "profile"},
		"buildinfo":   {},
		"connection":  {},
		"profile":     {},
		"profilejson": {"application", "profile"},
		"runner":      {"connection", "profile"},
		"tailscale":   {"connection", "profile"},
		"ui":          {"application", "buildinfo", "connection", "profile"},
		"wireproxy":   {"connection", "profile"},
	}

	for packageName, allowed := range rules {
		t.Run(packageName, func(t *testing.T) {
			assertAllowedInternalImports(t, root, packageName, allowed)
		})
	}
}

func assertAllowedInternalImports(t *testing.T, root, packageName string, allowed []string) {
	t.Helper()
	allowedSet := make(map[string]bool, len(allowed))
	for _, item := range allowed {
		allowedSet[modulePath+"/internal/"+item] = true
	}

	dir := filepath.Join(root, "internal", packageName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var violations []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Decls {
			declaration, ok := spec.(*ast.GenDecl)
			if !ok || declaration.Tok != token.IMPORT {
				continue
			}
			for _, importSpec := range declaration.Specs {
				pathValue, ok := importSpec.(*ast.ImportSpec)
				if !ok {
					continue
				}
				importPath, err := strconv.Unquote(pathValue.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(importPath, modulePath+"/internal/") && !allowedSet[importPath] {
					violations = append(violations, entry.Name()+" imports "+importPath)
				}
			}
		}
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("package crosses its architecture boundary:\n%s", strings.Join(violations, "\n"))
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate architecture test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
