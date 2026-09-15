package ui

import (
	"strings"
	"testing"
)

// TestMediaIsARelationWithItsOwnRenderer: Media shares the value source,
// /options/{field} and hydration with Relation - IsRelation() checks that;
// the renderer (thumbnail instead of label) differs only by the ui:field
// value.
func TestMediaIsARelationWithItsOwnRenderer(t *testing.T) {
	f := Media("avatar", "1:1")
	if !f.IsRelation() {
		t.Error("Media must be a relation: options, hydration and /options/{field} are shared with Relation")
	}
	if f["ui:field"] != MediaFieldKey {
		t.Errorf("ui:field = %v, want %q", f["ui:field"], MediaFieldKey)
	}
	opts, _ := f["ui:options"].(map[string]any)
	if opts["collection"] != "avatar" || opts["aspect"] != "1:1" {
		t.Errorf("ui:options = %v, want collection=avatar aspect=1:1", opts)
	}
}

// TestMediaKeepsOptionsWhenLabelsAreAdded: RelationLabels merges labels into
// ui:options rather than replacing the map (see RelationLabels in
// relation.go) - the same hatch on Media must leave collection/aspect in
// place, otherwise the widget would silently stop searching and drawing
// previews of the right ratio.
func TestMediaKeepsOptionsWhenLabelsAreAdded(t *testing.T) {
	f := Media("avatar", "1:1").RelationLabels(map[string]string{"1": "https://example.com/thumb.jpg"})

	opts, ok := f["ui:options"].(map[string]any)
	if !ok {
		t.Fatalf("field has no ui:options: %#v", f)
	}
	if opts["collection"] != "avatar" {
		t.Errorf("collection lost after RelationLabels: %#v", opts)
	}
	if opts["aspect"] != "1:1" {
		t.Errorf("aspect lost after RelationLabels: %#v", opts)
	}
	labels, ok := opts["labels"].(map[string]string)
	if !ok || labels["1"] != "https://example.com/thumb.jpg" {
		t.Errorf("labels = %#v", opts["labels"])
	}
}

// TestMediaNullable: Nullable is the shared modifier wrapping "type" into
// ["string","null"]: a media id column admits NULL like any other relation
// id.
func TestMediaNullable(t *testing.T) {
	f := Media("avatar", "1:1").Nullable()

	types, ok := f["type"].([]string)
	if !ok || len(types) != 2 || types[0] != "string" || types[1] != "null" {
		t.Errorf("type = %v, want [string null]", f["type"])
	}
}

// TestMediaMultiKeepsFieldMarkerOnTheField: MediaMulti is an array of ids
// with the media renderer ON THE FIELD ITSELF: both file injection (the
// engine's media-field detection) and label hydration (IsRelation) read the
// top-level ui:field, and nesting would blind both.
func TestMediaMultiKeepsFieldMarkerOnTheField(t *testing.T) {
	f := MediaMulti("trip_gallery", "2:1")

	if f["ui:field"] != MediaFieldKey {
		t.Errorf("ui:field = %v, want %q", f["ui:field"], MediaFieldKey)
	}
	if f["type"] != "array" {
		t.Errorf("type = %v, want array", f["type"])
	}
	if f["uniqueItems"] != true {
		t.Errorf("uniqueItems = %v, want true", f["uniqueItems"])
	}
	if !f.IsRelation() {
		t.Error("IsRelation() = false, want true")
	}
	opts, _ := f["ui:options"].(map[string]any)
	if opts["multi"] != true || opts["collection"] != "trip_gallery" || opts["aspect"] != "2:1" {
		t.Errorf("ui:options = %v", opts)
	}
}

// TestMediaMultiWireIsListOfStrings: the wire shape is a list of strings -
// the erjet.ChildValues cell over UUID declares exactly that, and a drift
// goes red in decl.Lint.
func TestMediaMultiWireIsListOfStrings(t *testing.T) {
	got := MediaMulti("trip_gallery", "2:1").WireKind()

	if got.Kind != KindList || got.Elem != KindString {
		t.Errorf("wire = %+v, want {list, string}", got)
	}
}

// TestMediaMultiKeepsOptionsThroughModifiers: modifiers applied after the
// constructor must not lose ui:options.
func TestMediaMultiKeepsOptionsThroughModifiers(t *testing.T) {
	f := MediaMulti("trip_gallery", "2:1").MaxBytes(1234).RelationLabels(map[string]string{"a": "u"})

	opts, _ := f["ui:options"].(map[string]any)
	if opts["multi"] != true || opts["aspect"] != "2:1" || opts["maxBytes"] != int64(1234) {
		t.Errorf("ui:options = %v", opts)
	}
	labels, _ := opts["labels"].(map[string]string)
	if labels["a"] != "u" {
		t.Errorf("labels = %v", labels)
	}
}

// TestLintRejectsMediaMultiWithUnknownAspect: an unknown aspect on multi
// media goes red in Lint just as on single - lintMedia keys on ui:field, not
// cardinality.
func TestLintRejectsMediaMultiWithUnknownAspect(t *testing.T) {
	got := Lint(map[string]Field{"gallery": MediaMulti("trip_gallery", "16:9")})
	if len(got) != 1 || !strings.Contains(got[0], "aspect") {
		t.Errorf("Lint = %v, want one problem mentioning aspect", got)
	}
}
