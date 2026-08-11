package agentassessment

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageCannotImportHumanReviewOrPromotion(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", entry.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			path := strings.Trim(spec.Path.Value, "\"")
			if strings.HasSuffix(path, "/internal/review") ||
				strings.HasSuffix(path, "/internal/promotion") {
				t.Fatalf("%s imports forbidden authority package %s", entry.Name(), path)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if ok && selector.Sel.Name == "RecordAttestation" {
				t.Fatalf("%s calls forbidden evaluation authority API RecordAttestation", entry.Name())
			}
			if ok && selector.Sel.Name == "Append" {
				t.Fatalf("%s calls a forbidden append method", entry.Name())
			}
			return true
		})
	}
}
