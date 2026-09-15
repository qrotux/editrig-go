package validate

import (
	"encoding/json"
	"testing"
)

// Cardinality is read from ui:options.multi of the same uiSchema entry that
// carries ui:field.
func TestMediaFieldKind(t *testing.T) {
	ui := json.RawMessage(`{
		"cover_id": {"ui:field":"media","ui:options":{"multi":false}},
		"gallery":  {"ui:field":"media","ui:options":{"multi":true}},
		"frozen":   {"ui:field":"media","ui:readonly":true,"ui:options":{"multi":true}},
		"title":    {"ui:widget":"textarea"}
	}`)

	for _, tc := range []struct {
		field string
		want  MediaKind
	}{
		{"cover_id", MediaSingle},
		{"gallery", MediaMulti},
		{"frozen", MediaNone},
		{"title", MediaNone},
		{"absent", MediaNone},
	} {
		if got := MediaFieldKind(ui, tc.field); got != tc.want {
			t.Errorf("MediaFieldKind(%q) = %v, want %v", tc.field, got, tc.want)
		}
	}
}

// An empty uiSchema (nil and `{}`) is no media field: an undeclared media
// field does not exist, it is not "single by default".
func TestMediaFieldKindEmptyUISchema(t *testing.T) {
	if MediaFieldKind(nil, "photo_id") != MediaNone {
		t.Error("MediaFieldKind(nil, ...) != MediaNone")
	}
	if MediaFieldKind(json.RawMessage(`{}`), "photo_id") != MediaNone {
		t.Error("MediaFieldKind({}, ...) != MediaNone")
	}
}
