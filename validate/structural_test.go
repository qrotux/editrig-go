package validate

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

const testSchema = `{
	"type": "object",
	"required": ["a"],
	"properties": {
		"a": {"type": "string"},
		"nested": {
			"type": "object",
			"properties": {
				"n": {"type": "integer"}
			}
		}
	}
}`

func TestStructuralValid(t *testing.T) {
	fe, err := Structural(json.RawMessage(testSchema), map[string]any{"a": "hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fe) != 0 {
		t.Fatalf("expected no FieldErrors, got %+v", fe)
	}
}

func TestStructuralRequiredAndNestedType(t *testing.T) {
	data := map[string]any{"nested": map[string]any{"n": "x"}}
	fe, err := Structural(json.RawMessage(testSchema), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fe) != 2 {
		t.Fatalf("expected 2 FieldErrors, got %d: %+v", len(fe), fe)
	}

	sort.Slice(fe, func(i, j int) bool { return fe[i].Field < fe[j].Field })

	if fe[0].Field != "/a" {
		t.Errorf("expected first FieldError.Field = /a, got %q", fe[0].Field)
	}
	if !strings.Contains(fe[0].Message, "a") {
		t.Errorf("expected required-message to mention %q, got %q", "a", fe[0].Message)
	}

	if fe[1].Field != "/nested/n" {
		t.Errorf("expected second FieldError.Field = /nested/n, got %q", fe[1].Field)
	}
	if fe[1].Message == "" {
		t.Errorf("expected non-empty type-error message")
	}
}

func TestCompileMalformedJSON(t *testing.T) {
	_, err := compile(json.RawMessage(`{"type": `))
	if err == nil {
		t.Fatalf("expected compile error for malformed schema JSON")
	}
}
