package jsonpath

import (
	"encoding/json"
	"testing"
)

func TestGetSetDelete(t *testing.T) {
	root := map[string]any{
		"orders": []any{map[string]any{"id": "order-1", "amount": float64(10)}},
	}
	if value, ok := Get(root, "$.orders[0].id"); !ok || value != "order-1" {
		t.Fatalf("get = %v, %t", value, ok)
	}
	if !Set(root, "$.orders[0].amount", "ten") {
		t.Fatal("set failed")
	}
	if value, _ := Get(root, "$.orders[0].amount"); value != "ten" {
		t.Fatalf("set value = %v", value)
	}
	if !Delete(root, "$.orders[0].id") {
		t.Fatal("delete failed")
	}
	if _, ok := Get(root, "$.orders[0].id"); ok {
		t.Fatal("deleted key still exists")
	}
}

func TestArrayDeletionPreservesShape(t *testing.T) {
	root := []any{"first", "second"}
	if value, ok := Get([]any{map[string]any{"name": "first"}}, "$[0].name"); !ok || value != "first" {
		t.Fatalf("root array lookup = %v, %t", value, ok)
	}
	if !Delete(root, "$[0]") || root[0] != nil || root[1] != "second" {
		t.Fatalf("root = %#v", root)
	}
}

func TestRejectsUnsupportedAndMissingPaths(t *testing.T) {
	root := map[string]any{"a": float64(1)}
	for _, path := range []string{"", "a", "$..a", "$.items[*]", "$.items[?(@.id)]", "$.", "$.a[0]b", "$[0]a", "$.a.[0]"} {
		if Validate(path) == nil {
			t.Fatalf("expected %q to be rejected", path)
		}
	}
	if Set(root, "$.missing", true) || Delete(root, "$.missing") {
		t.Fatal("missing path was mutated")
	}
}

func FuzzOperations(f *testing.F) {
	f.Add(`{"a":[{"b":"value"}]}`, "$.a[0].b")
	f.Fuzz(func(t *testing.T, document, path string) {
		var root any
		if json.Unmarshal([]byte(document), &root) != nil {
			return
		}
		_, _ = Get(root, path)
		_ = Set(root, path, "replacement")
		_ = Delete(root, path)
	})
}
