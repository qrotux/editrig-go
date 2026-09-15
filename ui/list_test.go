package ui

import (
	"testing"
)

func TestIntegerAndLocalTimestampShape(t *testing.T) {
	if got := Integer()["type"]; got != "integer" {
		t.Errorf("Integer type = %v, want integer", got)
	}
	lt := LocalTimestamp()
	if got := lt["type"]; got != "string" {
		t.Errorf("LocalTimestamp type = %v, want string", got)
	}
	if got := lt["ui:widget"]; got != "localDatetime" {
		t.Errorf("LocalTimestamp widget = %v, want localDatetime", got)
	}
	// format: date-time requires an offset, which wall-clock time lacks. The
	// annotation would lie, and the browser hint would get in the way of input.
	if _, has := lt["format"]; has {
		t.Errorf("LocalTimestamp declares a format: %v — wall-clock is not RFC3339", lt["format"])
	}
}

// TestListInnerUIReachesItems: rjsf takes an element's uiSchema ONLY from
// uiSchema.items (computeItemUiSchema); put anywhere else, the element's
// widget would silently become the default text input.
func TestListInnerUIReachesItems(t *testing.T) {
	props, uiEntries := Split(map[string]Field{
		"tags": List(String().Widget("textarea")),
	})

	prop := props["tags"].(map[string]any)
	if prop["type"] != "array" {
		t.Errorf("schema type = %v, want array", prop["type"])
	}
	items, ok := prop["items"].(map[string]any)
	if !ok || items["type"] != "string" {
		t.Fatalf("schema items = %#v, want {type: string}", prop["items"])
	}
	if _, leaked := prop[itemUIKey]; leaked {
		t.Errorf("%s leaked into the schema half: %#v", itemUIKey, prop)
	}

	entry, ok := uiEntries["tags"].(map[string]any)
	if !ok {
		t.Fatal("no uiSchema entry for the list")
	}
	uiItems, ok := entry["items"].(map[string]any)
	if !ok {
		t.Fatalf("uiSchema entry = %#v, want an items key — rjsf reads nothing else", entry)
	}
	if uiItems["ui:widget"] != "textarea" {
		t.Errorf("uiSchema.items = %#v, want ui:widget textarea", uiItems)
	}
}

func TestListCarriesInnerWidgetForTimestamps(t *testing.T) {
	_, uiEntries := Split(map[string]Field{
		"stamps": List(Timestamp().Widget("datetime")),
	})
	entry := uiEntries["stamps"].(map[string]any)
	items, ok := entry["items"].(map[string]any)
	if !ok || items["ui:widget"] != "datetime" {
		t.Errorf("uiSchema.items = %#v, want ui:widget datetime", entry["items"])
	}
}

func TestListCarriesInnerWidgetForNumbers(t *testing.T) {
	_, uiEntries := Split(map[string]Field{"nums": List(Number())})
	entry := uiEntries["nums"].(map[string]any)
	items, ok := entry["items"].(map[string]any)
	if !ok || items["ui:widget"] != "number" {
		t.Errorf("uiSchema.items = %#v, want ui:widget number", entry["items"])
	}
}

// TestListWithoutInnerUIHasNoItemsEntry: an inner field without ui keys must
// not produce an empty items - it would add golden noise exactly like an empty
// ui entry (see len(entry) > 0 in Split). List(String()) produces no ui entry
// at all (String() without modifiers has an empty ui half), so the absence of
// the entry itself is checked, not of a hypothetical items inside it.
func TestListWithoutInnerUIHasNoItemsEntry(t *testing.T) {
	_, uiEntries := Split(map[string]Field{"strs": List(String())})
	if entry, has := uiEntries["strs"]; has {
		t.Errorf("uiSchema entry = %#v, want no entry for a bare inner field", entry)
	}
}

// TestMaxItemsReachesEveryArrayShape: the length cap must reach the schema
// half of ALL array kinds: there are four, built differently (wrapper, element
// marker, relation), and no shared code guarantees it.
//
// The schema half is checked, not the Field itself: only it goes to the
// validators - ajv on the client and structural validation on the server
// (validate.go) - so that is where the cap gets its force.
func TestMaxItemsReachesEveryArrayShape(t *testing.T) {
	props, _ := Split(map[string]Field{
		"strs":   List(String()).MaxItems(3),
		"rows":   Items().MaxItems(4),
		"rels":   Relation("tags", true).MaxItems(5),
		"images": MediaMulti("gallery", "2:1").MaxItems(6),
	})

	for name, want := range map[string]int{"strs": 3, "rows": 4, "rels": 5, "images": 6} {
		prop, _ := props[name].(map[string]any)
		if prop["maxItems"] != want {
			t.Errorf("%s maxItems = %v, want %d", name, prop["maxItems"], want)
		}
	}
}

// TestKeyedCarriesInnerMaxItems: for a keyed field with a list inside the cap
// is declared on the INNER list: the outer type is object, and JSON Schema
// would silently ignore maxItems on it (the lint does not even allow that key
// on an object). Keyed copies the inner schema half into every key - this
// test pins that the cap is part of the copy.
func TestKeyedCarriesInnerMaxItems(t *testing.T) {
	props, _ := Split(map[string]Field{
		"included": Keyed(List(String()).MaxItems(50), Keys("en", "ru")),
	})

	byKey, _ := props["included"].(map[string]any)["properties"].(map[string]any)
	for _, key := range []string{"en", "ru"} {
		prop, _ := byKey[key].(map[string]any)
		if prop["maxItems"] != 50 {
			t.Errorf("included.%s = %#v, want maxItems 50", key, prop)
		}
	}
}

func TestListNullable(t *testing.T) {
	f := List(String()).Nullable()
	types, ok := f["type"].([]string)
	if !ok || len(types) != 2 || types[0] != "array" || types[1] != "null" {
		t.Errorf("type = %#v, want [array null]", f["type"])
	}
}

func TestListRejectsRequiredInner(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("List accepted a Required inner field — want panic")
		}
	}()
	List(String().Required())
}

// TestListRejectsNullableInner: a nullable ELEMENT is not the same as a
// nullable LIST (List(inner).Nullable()): the schema would let null into the
// array, the cell would refuse to write the whole slice, and the form would
// "save" writing nothing. See ui.List.
func TestListRejectsNullableInner(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("List accepted a Nullable inner field — want panic")
		}
	}()
	List(String().Nullable())
}

func TestPropRejectsItemUIKey(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Errorf("Prop accepted %s — it is not a schema keyword", itemUIKey)
		}
	}()
	String().Prop(map[string]any{itemUIKey: map[string]any{}})
}

func TestLintAcceptsNewFields(t *testing.T) {
	problems := Lint(map[string]Field{
		"qty":     Integer().Min(0),
		"localAt": LocalTimestamp().Nullable(),
		"tags":    List(String()),
	})
	if len(problems) != 0 {
		t.Errorf("Lint = %v, want none", problems)
	}
}

// TestLintIgnoresItemUIMarker is separate and has a ui key INSIDE:
// List(String()) produces no $itemUI marker at all, so the previous test does
// not touch this branch. Lint works on the Field BEFORE Split, i.e. it sees
// the marker - and without an explicit skip it would report `keyword
// "$itemUI" does not apply to type array`.
func TestLintIgnoresItemUIMarker(t *testing.T) {
	problems := Lint(map[string]Field{
		"stamps": List(Timestamp().Widget("datetime")),
		"notes":  List(String().Widget("textarea")).Nullable(),
	})
	if len(problems) != 0 {
		t.Errorf("Lint = %v, want none — %s is a build marker, not a schema keyword", problems, itemUIKey)
	}
}
