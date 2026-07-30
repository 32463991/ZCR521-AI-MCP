package schema

import (
	"encoding/json"
	"regexp"
	"testing"
)

func TestRegistryIsCompleteAndUnique(t *testing.T) {
	registry := All("2026-07-28")
	if got, want := len(registry.Tools), 48; got != want {
		t.Fatalf("tool count %d, want %d", got, want)
	}
	seen := map[string]bool{}
	canonicalName := regexp.MustCompile(`^zcr521_[a-z0-9]+(?:_[a-z0-9]+)*$`)
	for _, tool := range registry.Tools {
		if seen[tool.Name] {
			t.Fatalf("duplicate tool %s", tool.Name)
		}
		seen[tool.Name] = true
		if len(tool.Name) > 64 || !canonicalName.MatchString(tool.Name) {
			t.Fatalf("non-canonical tool name %q", tool.Name)
		}
		if tool.Title == "" || tool.Title == tool.Name {
			t.Fatalf("%s lacks a human-readable title", tool.Name)
		}
		if tool.InputSchema["$schema"] != Draft202012 {
			t.Fatalf("%s lacks JSON Schema 2020-12 marker", tool.Name)
		}
		if _, ok := tool.InputSchema["oneOf"]; !ok {
			t.Fatalf("%s lacks action oneOf", tool.Name)
		}
		if _, err := json.Marshal(tool); err != nil {
			t.Fatalf("%s is not serializable: %v", tool.Name, err)
		}
	}
}

func TestValidateInvocation(t *testing.T) {
	if err := ValidateInvocation("zcr521_fs_hash", map[string]any{"action": "calculate"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateInvocation("zcr521_fs_hash", map[string]any{"action": "delete"}); err == nil {
		t.Fatal("expected invalid action")
	}
}
