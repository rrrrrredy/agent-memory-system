package schemas_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSchemasAreValidJSONAndLocalReferencesExist(t *testing.T) {
	paths, err := filepath.Glob("*.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no schemas found")
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatalf("%s is not valid JSON: %v", path, err)
		}
		visitReferences(t, path, document)
	}
}

func visitReferences(t *testing.T, schemaPath string, value any) {
	t.Helper()
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			visitReferences(t, schemaPath, item)
		}
	case map[string]any:
		for key, item := range typed {
			if key == "$ref" {
				reference, _ := item.(string)
				if reference == "" || strings.HasPrefix(reference, "http://") ||
					strings.HasPrefix(reference, "https://") {
					continue
				}
				parts := strings.SplitN(reference, "#", 2)
				targetPath := schemaPath
				targetDocument := any(nil)
				if parts[0] == "" {
					data, err := os.ReadFile(schemaPath)
					if err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(data, &targetDocument); err != nil {
						t.Fatal(err)
					}
				} else {
					targetPath = filepath.Join(filepath.Dir(schemaPath), filepath.FromSlash(parts[0]))
					data, err := os.ReadFile(targetPath)
					if err != nil {
						t.Fatalf("local schema reference %q is unavailable: %v", reference, err)
					}
					if err := json.Unmarshal(data, &targetDocument); err != nil {
						t.Fatalf("local schema reference %q is invalid JSON: %v", reference, err)
					}
				}
				if len(parts) == 2 && parts[1] != "" && !jsonPointerExists(targetDocument, parts[1]) {
					t.Fatalf("local schema reference %q has an unavailable fragment in %s",
						reference, targetPath)
				}
				continue
			}
			visitReferences(t, schemaPath, item)
		}
	}
}

func jsonPointerExists(document any, pointer string) bool {
	if pointer == "" {
		return true
	}
	if !strings.HasPrefix(pointer, "/") {
		return false
	}
	current := document
	for _, encoded := range strings.Split(pointer[1:], "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		switch typed := current.(type) {
		case map[string]any:
			value, ok := typed[token]
			if !ok {
				return false
			}
			current = value
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(typed) {
				return false
			}
			current = typed[index]
		default:
			return false
		}
	}
	return true
}
