package ui

import "testing"

func TestGeopointHalves(t *testing.T) {
	props, uiEntries := Split(map[string]Field{"location": Geopoint()})
	prop, _ := props["location"].(map[string]any)
	if _, typed := prop["type"]; typed {
		t.Errorf("schema half declares a type: %v", prop)
	}
	entry, _ := uiEntries["location"].(map[string]any)
	if entry["ui:field"] != GeopointFieldKey {
		t.Errorf("ui half: ui:field = %v, want %q", entry["ui:field"], GeopointFieldKey)
	}
}

func TestGeopointFieldKeyValue(t *testing.T) {
	// The value mirrors the client's field registry key.
	if GeopointFieldKey != "geopoint" {
		t.Errorf("GeopointFieldKey = %q, want \"geopoint\"", GeopointFieldKey)
	}
}

// TestGeopointReadonlySurvives: ui:field is set FIRST, before modifiers, so
// .Readonly() does not override it with readonlyDisplay (the same guarantee
// as JSON()).
func TestGeopointReadonlySurvives(t *testing.T) {
	f := Geopoint().Readonly()
	if f["ui:field"] != GeopointFieldKey {
		t.Errorf("Readonly() lost ui:field: %v", f["ui:field"])
	}
	if f["ui:widget"] == "readonlyDisplay" {
		t.Error("Readonly() overwrote the field renderer with readonlyDisplay")
	}
}
