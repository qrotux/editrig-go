package ui_test

import (
	"testing"

	"github.com/qrotux/editrig-go/ui"
)

func TestWireKind(t *testing.T) {
	cases := []struct {
		name  string
		field ui.Field
		want  ui.Wire
	}{
		{"string", ui.String(), ui.Wire{Kind: ui.KindString}},
		{"nullable string keeps its kind", ui.String().Nullable(), ui.Wire{Kind: ui.KindString}},
		{"integer is not number", ui.Integer(), ui.Wire{Kind: ui.KindInteger}},
		{"list of strings", ui.List(ui.String()), ui.Wire{Kind: ui.KindList, Elem: ui.KindString}},
		{"list of integers", ui.List(ui.Integer()), ui.Wire{Kind: ui.KindList, Elem: ui.KindInteger}},
		{"multi relation is a list of strings", ui.Relation("tags", true), ui.Wire{Kind: ui.KindList, Elem: ui.KindString}},
		{"single relation is a string", ui.Relation("tags", false), ui.Wire{Kind: ui.KindString}},
		{"keyed carries the inner kind", ui.Keyed(ui.String(), ui.Keys("en", "ru")), ui.Wire{Kind: ui.KindMap, Elem: ui.KindString}},
		// A composite inner type must read as ITS OWN shape, not degrade to
		// KindAny: KindAny in Elem short-circuits Match, so any map cell would
		// accept a map of lists, KeyedStrings included (which on Save treats a
		// non-string as a clear and deletes the translation of every submitted
		// locale).
		{"keyed list is a map of lists", ui.Keyed(ui.List(ui.String()), ui.Keys("en", "ru")), ui.Wire{Kind: ui.KindMap, Elem: ui.KindList}},
		{"keyed keyed is a map of maps", ui.Keyed(ui.Keyed(ui.String(), ui.Keys("a", "b")), ui.Keys("en")), ui.Wire{Kind: ui.KindMap, Elem: ui.KindMap}},
		// Wire carries no second nesting level (a single Elem) - the element of
		// the INNER list is not declared on the wire at all. Pinned by a test
		// because it is a deliberate limit, not a forgotten branch: narrowing it
		// means growing Wire itself.
		{"list of lists stops at one level", ui.List(ui.List(ui.String())), ui.Wire{Kind: ui.KindList, Elem: ui.KindList}},
		{"json declares nothing", ui.JSON(), ui.Wire{Kind: ui.KindAny}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.field.WireKind(); got != c.want {
				t.Errorf("WireKind() = %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestKeyedItemsWireKind: the marker on the OUTER field - the type axis sees
// {KindMap, KindItems}, exactly what erjet.KeyedChildRows declares.
func TestKeyedItemsWireKind(t *testing.T) {
	f := ui.Keyed(ui.Items(), ui.Keys("en", "ru"))
	if !f.IsKeyedItems() {
		t.Fatal("Keyed(Items()) is not recognised as keyed-items")
	}
	if got := f.WireKind(); got != (ui.Wire{Kind: ui.KindMap, Elem: ui.KindItems}) {
		t.Errorf("WireKind = %+v, want {map items}", got)
	}
	plain := ui.Keyed(ui.String(), ui.Keys("en"))
	if plain.IsKeyedItems() {
		t.Error("Keyed(String()) must not be keyed-items")
	}
	if got := plain.WireKind(); got != (ui.Wire{Kind: ui.KindMap, Elem: ui.KindString}) {
		t.Errorf("plain Keyed WireKind = %+v, want {map string}", got)
	}
	if ui.Items().IsKeyedItems() {
		t.Error("bare Items() must not be keyed-items")
	}
}

// TestWireMatchIsSymmetricAndAnyWildcards: KindAny means "not checked", and
// it must work from both sides. Otherwise a read-only cell (ReadOnly accepts
// anything) would go red on every field.
func TestWireMatchIsSymmetricAndAnyWildcards(t *testing.T) {
	str := ui.Wire{Kind: ui.KindString}
	list := ui.Wire{Kind: ui.KindList, Elem: ui.KindString}
	anyW := ui.Wire{Kind: ui.KindAny}

	if str.Match(list) || list.Match(str) {
		t.Error("string and list must not match in either direction")
	}
	if !anyW.Match(list) || !list.Match(anyW) {
		t.Error("KindAny must match anything in both directions")
	}
	ints := ui.Wire{Kind: ui.KindList, Elem: ui.KindInteger}
	if list.Match(ints) {
		t.Error("list of strings must not match list of integers")
	}

	// A map of LISTS against a map of STRINGS - the same axis as the two lists
	// above, but the price of a drift here is not "the field silently did not
	// save" but data loss: the map-of-strings cell (erjet.KeyedStrings) parses
	// []any as "", and "" there means CLEAR - every submitted locale would
	// lose its translation.
	mapOfLists := ui.Wire{Kind: ui.KindMap, Elem: ui.KindList}
	mapOfStrings := ui.Wire{Kind: ui.KindMap, Elem: ui.KindString}
	if mapOfLists.Match(mapOfStrings) || mapOfStrings.Match(mapOfLists) {
		t.Error("map of lists and map of strings must not match in either direction")
	}
}
