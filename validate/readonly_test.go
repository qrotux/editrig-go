package validate

import (
	"encoding/json"
	"testing"
)

func TestReadonlyFields(t *testing.T) {
	cases := []struct {
		name string
		ui   string
		want int
	}{
		{"declared", `{"rating":{"ui:readonly":true},"last_seen_at":{"ui:readonly":true}}`, 2},
		{"absent", `{"ui:order":["a"]}`, 0},
		{"empty uiSchema", ``, 0},
		{"broken json", `{`, 0},
		// Root extensions are not fields: a property name cannot start with "ui:".
		{"root extensions skipped", `{"ui:groups":[{"id":"g"}],"ui:header":{"titleField":"n"}}`, 0},
		// A field entry that is not an object (or lacks ui:readonly) is skipped.
		{"entry not an object", `{"a":"x"}`, 0},
		{"readonly false", `{"a":{"ui:readonly":false},"b":{"ui:widget":"hidden"}}`, 0},
		// A render-only flag without ui:readonly strips nothing.
		{"display only", `{"a":{"ui:widget":"readonlyDisplay"}}`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ReadonlyFields(json.RawMessage(c.ui)); len(got) != c.want {
				t.Errorf("ReadonlyFields(%s) = %v, want %d entries", c.ui, got, c.want)
			}
		})
	}
}

// TestReadonlyFieldsAreSorted: map order is random and the list ends up in
// dropped[] and from there in the handler log; unsorted, the log would jump
// from request to request.
func TestReadonlyFieldsAreSorted(t *testing.T) {
	got := ReadonlyFields(json.RawMessage(
		`{"zulu":{"ui:readonly":true},"alpha":{"ui:readonly":true},"mike":{"ui:readonly":true}}`))
	want := []string{"alpha", "mike", "zulu"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestStripReadonly(t *testing.T) {
	ui := json.RawMessage(`{"rating":{"ui:readonly":true},"missing":{"ui:readonly":true}}`)
	in := map[string]any{"name": "x", "rating": 4.5}
	dropped := StripReadonly(ui, in)

	if len(dropped) != 1 || dropped[0] != "rating" {
		t.Errorf("dropped = %v, want [rating]", dropped)
	}
	if _, still := in["rating"]; still {
		t.Errorf("rating survived the strip: %v", in)
	}
	if in["name"] != "x" {
		t.Errorf("editable field lost: %v", in)
	}
	if got := StripReadonly(ui, nil); got != nil {
		t.Errorf("StripReadonly(nil map) = %v, want nil", got)
	}
}
