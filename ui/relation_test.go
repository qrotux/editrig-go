package ui

import (
	"strings"
	"testing"
)

func relOptions(t *testing.T, f Field) map[string]any {
	t.Helper()
	opts, ok := f["ui:options"].(map[string]any)
	if !ok {
		t.Fatalf("field has no ui:options: %#v", f)
	}
	return opts
}

// TestRelationSingleShape: a single relation is a plain string property: the
// value on the wire is an id. The renderer comes via ui:field, not
// ui:widget, for a SINGLE renderer shared with multi (whose schema is an
// array, and rjsf routes a widget only for leaves).
func TestRelationSingleShape(t *testing.T) {
	f := Relation("countries", false)

	if f["type"] != "string" {
		t.Errorf("type = %v, want string", f["type"])
	}
	if f["ui:field"] != RelationFieldKey {
		t.Errorf("ui:field = %v, want %q", f["ui:field"], RelationFieldKey)
	}
	opts := relOptions(t, f)
	if opts["collection"] != "countries" {
		t.Errorf("collection = %v", opts["collection"])
	}
	if opts["multi"] != false {
		t.Errorf("multi = %v, want false", opts["multi"])
	}
}

// TestRelationMultiShape: a multi relation is an array of strings whose ORDER
// is significant (it is the order of relations in storage). uniqueItems
// because a duplicate chip in the form is indistinguishable from one chip,
// yet in the relation table it would double the row.
func TestRelationMultiShape(t *testing.T) {
	f := Relation("tags", true)

	if f["type"] != "array" {
		t.Errorf("type = %v, want array", f["type"])
	}
	items, ok := f["items"].(map[string]any)
	if !ok || items["type"] != "string" {
		t.Errorf("items = %#v, want {type:string}", f["items"])
	}
	if f["uniqueItems"] != true {
		t.Errorf("uniqueItems = %v, want true", f["uniqueItems"])
	}
	if relOptions(t, f)["multi"] != true {
		t.Errorf("multi flag missing for a multi relation")
	}
}

// TestRelationLabelsMerge: labels of selected values are added PER REQUEST
// (they are localized and depend on data) - by merging, not through the .UI
// hatch: that would replace ui:options wholesale and wipe collection, after
// which the widget would silently stop searching.
func TestRelationLabelsMerge(t *testing.T) {
	f := Relation("tags", true).RelationLabels(map[string]string{"1": "Пляж"})

	opts := relOptions(t, f)
	if opts["collection"] != "tags" {
		t.Errorf("collection lost after RelationLabels: %#v", opts)
	}
	labels, ok := opts["labels"].(map[string]string)
	if !ok || labels["1"] != "Пляж" {
		t.Errorf("labels = %#v", opts["labels"])
	}
}

// TestRelationLabelsDoNotLeak: copy-on-write modifiers - label hydration on
// one request must not leak into the field handed to the next.
func TestRelationLabelsDoNotLeak(t *testing.T) {
	base := Relation("tags", true)
	_ = base.RelationLabels(map[string]string{"1": "Пляж"})

	if _, leaked := relOptions(t, base)["labels"]; leaked {
		t.Error("RelationLabels mutated the shared field")
	}
}

// TestRelationParentFieldMerge: merging rather than the .UI hatch, for the
// same reason as RelationLabels: replacing ui:options wholesale would wipe
// collection/multi, and the widget would silently stop searching.
func TestRelationParentFieldMerge(t *testing.T) {
	f := Relation("trip_entries", false).ParentField("trip_id")

	opts := relOptions(t, f)
	if opts["collection"] != "trip_entries" || opts["multi"] != false {
		t.Errorf("collection/multi lost after ParentField: %#v", opts)
	}
	if opts["parentField"] != "trip_id" {
		t.Errorf("parentField = %#v, want trip_id", opts["parentField"])
	}
}

// TestRelationParentFieldDoesNotLeak: copy-on-write modifiers - one field's
// scope must not leak into a field built from the same base.
func TestRelationParentFieldDoesNotLeak(t *testing.T) {
	base := Relation("trip_entries", false)
	_ = base.ParentField("trip_id")

	if _, leaked := relOptions(t, base)["parentField"]; leaked {
		t.Error("ParentField mutated the shared field")
	}
}

func TestLintAcceptsRelations(t *testing.T) {
	fields := map[string]Field{
		"country_id": Relation("countries", false).Nullable(),
		"interests":  Relation("tags", true),
	}
	if problems := Lint(fields); len(problems) != 0 {
		t.Errorf("Lint = %v, want none", problems)
	}
}

// TestLintRejectsRelationWithoutCollection: a relation without a collection
// has nowhere to take options from: the widget shows an empty list, and there
// is no error anywhere.
func TestLintRejectsRelationWithoutCollection(t *testing.T) {
	f := Relation("tags", true)
	delete(f["ui:options"].(map[string]any), "collection")

	problems := Lint(map[string]Field{"interests": f})
	if len(problems) == 0 || !strings.Contains(problems[0], "collection") {
		t.Errorf("Lint = %v, want a complaint about the missing collection", problems)
	}
}

// TestLintRejectsCardinalityMismatch: cardinality is declared TWICE - by the
// schema type and by the multi flag - and they must not drift: the widget
// reads the flag, the validator the type, so the form would send an array
// into a string property and get a 422 out of nowhere.
func TestLintRejectsCardinalityMismatch(t *testing.T) {
	multiAsScalar := Relation("tags", true)
	multiAsScalar["type"] = "string"
	delete(multiAsScalar, "items")
	delete(multiAsScalar, "uniqueItems")

	singleAsArray := Relation("countries", false)
	singleAsArray["type"] = "array"

	for name, f := range map[string]Field{"interests": multiAsScalar, "country_id": singleAsArray} {
		problems := Lint(map[string]Field{name: f})
		if len(problems) == 0 {
			t.Errorf("%s: Lint accepted a cardinality mismatch", name)
		}
	}
}

// TestLintKnowsArrayKeywords: the array type is in the applicability table:
// without it items/uniqueItems would count as inapplicable keys, and
// maxLength on an array as allowed.
func TestLintKnowsArrayKeywords(t *testing.T) {
	ok := Field{"type": "array", "items": map[string]any{"type": "string"}, "uniqueItems": true, "minItems": 1}
	if problems := Lint(map[string]Field{"tags": ok}); len(problems) != 0 {
		t.Errorf("Lint = %v, want none", problems)
	}

	bad := Field{"type": "array", "maxLength": 5}
	problems := Lint(map[string]Field{"tags": bad})
	if len(problems) == 0 || !strings.Contains(problems[0], "maxLength") {
		t.Errorf("Lint = %v, want a complaint about maxLength on an array", problems)
	}
}
