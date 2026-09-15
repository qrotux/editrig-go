package ui

import (
	"bytes"
	"encoding/json"
	"testing"
)

// decode returns both halves of the document as maps, for key-presence checks.
func decode(t *testing.T, d Document) (schema, uiSchema map[string]any) {
	t.Helper()
	sb, ub, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(sb, &schema); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(ub, &uiSchema); err != nil {
		t.Fatal(err)
	}
	return schema, uiSchema
}

// TestDocumentOmitsEmptyRootKeys: an unset root concept must DISAPPEAR from
// the document, not arrive as zero: "required": null is rejected by the schema
// compiler, and empty ui:groups/ui:header are not drawn by the client anyway -
// they would be noise on the wire and in goldens.
func TestDocumentOmitsEmptyRootKeys(t *testing.T) {
	schema, uiSchema := decode(t, Document{
		Fields: map[string]Field{"a": String()},
		Order:  []string{"a"},
	})

	for _, key := range []string{"required", "additionalProperties"} {
		if _, ok := schema[key]; ok {
			t.Errorf("schema carries %q, want it omitted", key)
		}
	}
	for _, key := range []string{GroupsKey, HeaderKey} {
		if _, ok := uiSchema[key]; ok {
			t.Errorf("uiSchema carries %q, want it omitted", key)
		}
	}
	if schema["type"] != "object" {
		t.Errorf("type = %v, want object", schema["type"])
	}
}

// TestDocumentOrderIsAlwaysEmitted: ui:order is needed by BOTH schemas, the
// short one included: without it rjsf takes the order of the incoming object,
// and Go marshals a map alphabetically ("name" < "username").
func TestDocumentOrderIsAlwaysEmitted(t *testing.T) {
	_, uiSchema := decode(t, Document{
		Fields: map[string]Field{"username": String(), "name": String()},
		Order:  []string{"username", "name"},
	})
	got, ok := uiSchema["ui:order"].([]any)
	if !ok {
		t.Fatalf("ui:order missing or not an array: %#v", uiSchema["ui:order"])
	}
	if len(got) != 2 || got[0] != "username" || got[1] != "name" {
		t.Errorf("ui:order = %v, want [username name] in declared order", got)
	}
}

// TestDocumentKeepsFieldUIEntries: per-field ui entries must reach the root
// of the uiSchema in ANY document. Guards against building a short schema via
// `props, _ := ui.Split(...)`, which DROPS the second half: with two fields
// lacking ui keys nothing shows, and the first .Placeholder()/.Widget() on
// them would silently vanish from the client.
func TestDocumentKeepsFieldUIEntries(t *testing.T) {
	_, uiSchema := decode(t, Document{
		Fields: map[string]Field{"username": String().Required().Placeholder("nickname")},
		Order:  []string{"username"},
	})
	entry, ok := uiSchema["username"].(map[string]any)
	if !ok {
		t.Fatalf("uiSchema has no entry for username: %#v", uiSchema)
	}
	if entry["ui:placeholder"] != "nickname" {
		t.Errorf("ui:placeholder = %v, want nickname", entry["ui:placeholder"])
	}
}

// TestRequiredFollowsOrder: the root "required" is built from the field
// markers in Order, not map-walk order: the array on the wire must be
// deterministic, otherwise a golden fixture would flicker on every run.
func TestRequiredFollowsOrder(t *testing.T) {
	schema, _ := decode(t, Document{
		Fields: map[string]Field{
			"username": String().Required(),
			"name":     String().Required(),
			"city":     String(),
		},
		// Reverse-alphabetical order: coincidence with the map walk ruled out.
		Order: []string{"username", "name", "city"},
	})
	req, _ := schema["required"].([]any)
	if len(req) != 2 || req[0] != "username" || req[1] != "name" {
		t.Errorf("required = %v, want [username name] in Order order", schema["required"])
	}
}

// TestRequiredNeverReachesTheWire: the required marker is a key of neither
// half: in JSON Schema "required" is an ARRAY on the parent, and "$required":
// true inside a property would be garbage (at best silently ignored, at worst
// rejected by the compiler along with the whole schema).
func TestRequiredNeverReachesTheWire(t *testing.T) {
	sb, ub, err := Document{
		Fields: map[string]Field{"a": String().Required().Placeholder("x")},
		Order:  []string{"a"},
	}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for half, raw := range map[string][]byte{"schema": sb, "uiSchema": ub} {
		if bytes.Contains(raw, []byte(requiredKey)) {
			t.Errorf("%s carries the %q marker: %s", half, requiredKey, raw)
		}
	}
	// And the positive half of the fact: requiredness is still declared -
	// otherwise the test would be green on a field that lost it entirely.
	if !bytes.Contains(sb, []byte(`"required":["a"]`)) {
		t.Errorf("schema lost the required array: %s", sb)
	}
}

// TestRequiredPropIsGuarded: the marker cannot be written as a raw key: it is
// not a schema property, and via .Prop it would bypass Marshal and do nothing.
func TestRequiredPropIsGuarded(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Prop accepted the required marker — want panic pointing at .Required()")
		}
	}()
	String().Prop(map[string]any{requiredKey: true})
}

// TestKeyedItemsMarkerNeverReachesTheWire: Split drops markers by an EXPLICIT
// list - this entry of the drop list is what is guarded here.
func TestKeyedItemsMarkerNeverReachesTheWire(t *testing.T) {
	sb, ub, err := Document{
		Fields: map[string]Field{"blocks": Keyed(Items(), Keys("en", "ru"))},
		Order:  []string{"blocks"},
	}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for half, raw := range map[string][]byte{"schema": sb, "uiSchema": ub} {
		if bytes.Contains(raw, []byte(keyedItemsKey)) {
			t.Errorf("%s carries the %q marker: %s", half, keyedItemsKey, raw)
		}
	}
}

// TestKeyedItemsPropIsGuarded: like TestRequiredPropIsGuarded, an assembly
// marker cannot be written as a raw key.
func TestKeyedItemsPropIsGuarded(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Prop accepted the keyed-items marker — want panic pointing at ui.Keyed(ui.Items(), …)")
		}
	}()
	String().Prop(map[string]any{keyedItemsKey: true})
}
