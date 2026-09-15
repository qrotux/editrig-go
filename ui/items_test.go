package ui_test

import (
	"encoding/json"
	"testing"

	"github.com/qrotux/editrig-go/ui"
)

// TestItemsCarriesBothHalves: the element's schema half goes to items, its ui
// half to uiSchema.items and NOWHERE else: that is the only key rjsf looks at
// for an array element (computeItemUiSchema).
func TestItemsCarriesBothHalves(t *testing.T) {
	field := ui.Items().WithItems(map[string]ui.Field{
		"day":   ui.Integer().Required().Min(1),
		"label": ui.String().Widget("textarea"),
	}, []string{"day", "label"})

	props, entries := ui.Split(map[string]ui.Field{"itinerary": field})

	prop := props["itinerary"].(map[string]any)
	if prop["type"] != "array" {
		t.Fatalf("type = %v, want array", prop["type"])
	}
	item := prop["items"].(map[string]any)
	if item["type"] != "object" || item["additionalProperties"] != false {
		t.Errorf("item = %v, want a closed object", item)
	}
	req, _ := item["required"].([]string)
	if len(req) != 1 || req[0] != "day" {
		t.Errorf("items.required = %v, want [day]", req)
	}
	if _, leaked := prop["$itemUI"]; leaked {
		t.Error("$itemUI must not reach the schema half")
	}

	entry := entries["itinerary"].(map[string]any)
	itemUI, ok := entry["items"].(map[string]any)
	if !ok {
		t.Fatalf("uiSchema entry has no items: %v", entry)
	}
	if order, _ := itemUI["ui:order"].([]string); len(order) != 2 || order[0] != "day" {
		t.Errorf("items.ui:order = %v, want [day label]", itemUI["ui:order"])
	}
	sub := itemUI["label"].(map[string]any)
	if sub["ui:widget"] != "textarea" {
		t.Errorf("subfield ui half lost its widget: %v", sub)
	}

	if _, err := json.Marshal(prop); err != nil {
		t.Fatalf("schema half does not marshal: %v", err)
	}
}

// TestItemsRequiredOnTheListItself: requiredness of the list ITSELF lives in
// the document's root required, requiredness of a subfield in items.required.
// Easy to confuse, and confused they give a form that cannot be submitted.
func TestItemsRequiredOnTheListItself(t *testing.T) {
	field := ui.Items().WithItems(map[string]ui.Field{"day": ui.Integer()}, []string{"day"}).Required()
	if !field.IsRequired() {
		t.Error("Required() on the list itself is not visible to the document builder")
	}
	props, _ := ui.Split(map[string]ui.Field{"x": field})
	item := props["x"].(map[string]any)["items"].(map[string]any)
	if _, ok := item["required"]; ok {
		t.Error("Required() on the list leaked into items.required")
	}
}

// TestItemsIsItems: the flag is needed both by document assembly (it fills in
// the element) and by the lint (it flags an unfilled list).
func TestItemsIsItems(t *testing.T) {
	if !ui.Items().IsItems() {
		t.Error("Items() is not recognised as an items field")
	}
	if ui.List(ui.String()).IsItems() {
		t.Error("List is not an items field")
	}
}

// TestWithKeyedItemsComposesBothHalves mirrors TestItemsCarriesBothHalves for
// the keyed case: the schema into every key's copy, the ui half into
// ui:options.inner.items (the only address the KeyedField -> ObjectField ->
// ArrayField.computeItemUiSchema chain reads it from).
func TestWithKeyedItemsComposesBothHalves(t *testing.T) {
	f := ui.Keyed(ui.Items(), ui.Keys("en", "ru")).WithKeyedItems(map[string]ui.Field{
		"day":   ui.Integer().Required(),
		"label": ui.String().Widget("textarea"),
	}, []string{"day", "label"})

	props, entries := ui.Split(map[string]ui.Field{"blocks": f})
	prop := props["blocks"].(map[string]any)
	for _, key := range []string{"en", "ru"} {
		kp := prop["properties"].(map[string]any)[key].(map[string]any)
		if kp["type"] != "array" {
			t.Fatalf("%s.type = %v, want array", key, kp["type"])
		}
		item := kp["items"].(map[string]any)
		if item["type"] != "object" || item["additionalProperties"] != false {
			t.Errorf("%s.items = %v, want a closed object", key, item)
		}
		if req, _ := item["required"].([]string); len(req) != 1 || req[0] != "day" {
			t.Errorf("%s.items.required = %v, want [day]", key, item["required"])
		}
	}
	// The copies are independent OF EACH OTHER.
	en := prop["properties"].(map[string]any)["en"].(map[string]any)["items"].(map[string]any)
	en["mutated"] = true
	ru := prop["properties"].(map[string]any)["ru"].(map[string]any)["items"].(map[string]any)
	if _, leaked := ru["mutated"]; leaked {
		t.Error("items schema is shared between keys")
	}

	entry := entries["blocks"].(map[string]any)
	inner := entry["ui:options"].(map[string]any)["inner"].(map[string]any)
	itemUI, ok := inner["items"].(map[string]any)
	if !ok {
		t.Fatalf("ui:options.inner has no items: %v", inner)
	}
	if order, _ := itemUI["ui:order"].([]string); len(order) != 2 || order[0] != "day" {
		t.Errorf("inner.items.ui:order = %v, want [day label]", itemUI["ui:order"])
	}
	if sub := itemUI["label"].(map[string]any); sub["ui:widget"] != "textarea" {
		t.Errorf("subfield ui half lost its widget: %v", sub)
	}
}

// TestWithKeyedItemsDoesNotMutateTheReceiver guards the most dangerous spot
// of the composition: clone is shallow, the declaration is package-level, and
// Fields is called per request. Mutating nested maps means concurrent map
// writes plus localized titles leaking between requests.
func TestWithKeyedItemsDoesNotMutateTheReceiver(t *testing.T) {
	base := ui.Keyed(ui.Items(), ui.Keys("en")).KeyedLayout(ui.KeyedChips)
	before, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}

	a := base.WithKeyedItems(map[string]ui.Field{"day": ui.Integer()}, []string{"day"})
	b := base.WithKeyedItems(map[string]ui.Field{"other": ui.String().Title("Чужой заголовок")}, []string{"other"})

	after, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("WithKeyedItems mutated the declaration field:\nbefore %s\nafter  %s", before, after)
	}
	// The results do not see each other (two requests with different catalogs).
	aProps := a["properties"].(map[string]any)["en"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	if _, leaked := aProps["other"]; leaked {
		t.Error("composition of one call leaked into another")
	}
	_ = b
	// Existing ui:options keys (layout) are not lost.
	if opts := a["ui:options"].(map[string]any); opts["layout"] != ui.KeyedChips {
		t.Errorf("layout lost after composition: %v", opts["layout"])
	}
}
