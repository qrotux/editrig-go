package ui

import (
	"reflect"
	"testing"
)

func TestIconHalves(t *testing.T) {
	props, uiEntries := Split(map[string]Field{"icon": Icon()})
	prop, _ := props["icon"].(map[string]any)
	if prop["type"] != "string" {
		t.Errorf("schema half: type = %v, want \"string\"", prop["type"])
	}
	if _, leaked := prop["ui:field"]; leaked {
		t.Error("schema half carries ui:field — Split did not separate the halves")
	}
	entry, _ := uiEntries["icon"].(map[string]any)
	if entry["ui:field"] != IconFieldKey {
		t.Errorf("ui half: ui:field = %v, want %q", entry["ui:field"], IconFieldKey)
	}
}

func TestIconFieldKeyValue(t *testing.T) {
	// The value mirrors the client's field registry key.
	if IconFieldKey != "icon" {
		t.Errorf("IconFieldKey = %q, want \"icon\"", IconFieldKey)
	}
}

func TestIconNullable(t *testing.T) {
	f := Icon().Nullable()
	if !reflect.DeepEqual(f["type"], []string{"string", "null"}) {
		t.Errorf("type = %v, want [string null]", f["type"])
	}
	// Without ui:emptyValue the key would drop out of the PATCH payload and
	// clearing the field would silently roll back (see Nullable in field.go).
	v, ok := f["ui:emptyValue"]
	if !ok || v != nil {
		t.Errorf("ui:emptyValue = %v (present=%v), want nil/present", v, ok)
	}
	if f["ui:field"] != IconFieldKey {
		t.Errorf("Nullable() lost ui:field: %v", f["ui:field"])
	}
}
