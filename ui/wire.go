package ui

// Kind is the SHAPE of a field value on the wire, coarsened to what a storage
// cell distinguishes. Neither a JSON Schema type nor a column type: the
// field-to-cell gate asks exactly one thing - "can this cell parse what the
// form sends?" - and eight values suffice to answer.
//
// It lives in ui rather than erjet because it is the language the two halves
// SHARE: the presentation declares the shape, the store accepts it, and decl
// compares them - decl imports ui and knows no driver.
type Kind string

const (
	KindString  Kind = "string"
	KindNumber  Kind = "number"
	KindInteger Kind = "integer"
	KindBool    Kind = "boolean"
	// KindList is an array of SCALARS: ui.List, ui.Relation(multi). Wire.Elem
	// carries the element shape: text[] and int4[] are both arrays on the wire
	// but their cells differ, and without the element the gate would let
	// ui.List(ui.Integer()) through onto erjet.Strings.
	KindList Kind = "list"
	// KindItems is an array of OBJECTS: ui.Items. The element is composite, so
	// Elem stays empty: a separate decl.Lint call over the nested declaration
	// checks the element's composition.
	KindItems Kind = "items"
	// KindMap is an object with fixed keys: ui.Keyed. Elem is the shape of one
	// key's VALUE.
	KindMap Kind = "map"
	// KindAny means no shape is declared (jsonb) or the cell deliberately
	// accepts anything (ReadOnly). It matches every other shape in both
	// directions: there is nothing to narrow it with, and a red gate here
	// would be false.
	KindAny Kind = "any"
)

// Wire is the declared shape as a whole. Elem is set only for KindList and
// KindMap.
type Wire struct {
	Kind Kind
	Elem Kind
}

// Match reports whether the field's declared shape and the shape a cell
// accepts are compatible. KindAny on either side matches: that is what "not
// checked" means.
//
// Symmetric on purpose: the gate has no "main" side, both halves are equal
// and declared one in ui, the other in the store.
func (w Wire) Match(other Wire) bool {
	if w.Kind == KindAny || other.Kind == KindAny {
		return true
	}
	if w.Kind != other.Kind {
		return false
	}
	if w.Elem == KindAny || other.Elem == KindAny {
		return true
	}
	return w.Elem == other.Elem
}

// WireKind is the shape the field declares on the wire.
//
// Read FROM THE SAME KEYS that go to the wire, not from a separate flag: a
// third list of shapes would drift from both documents - exactly the defect
// Field removes at the field level.
func (f Field) WireKind() Wire {
	switch {
	case f.IsItems():
		return Wire{Kind: KindItems}
	case f.IsKeyed():
		if f.IsKeyedItems() {
			// Object element: the nested decl.Lint over Entry.Items checks its
			// composition, so Elem is exactly KindItems - as for ui.Items above.
			return Wire{Kind: KindMap, Elem: KindItems}
		}
		return Wire{Kind: KindMap, Elem: innerKind(f)}
	case hasType(f["type"], "array"):
		return Wire{Kind: KindList, Elem: itemKind(f)}
	case hasType(f["type"], "string"):
		return Wire{Kind: KindString}
	case hasType(f["type"], "integer"):
		return Wire{Kind: KindInteger}
	case hasType(f["type"], "number"):
		return Wire{Kind: KindNumber}
	case hasType(f["type"], "boolean"):
		return Wire{Kind: KindBool}
	}
	return Wire{Kind: KindAny}
}

// itemKind is the array element shape, read from the schema half "items".
func itemKind(f Field) Kind {
	item, ok := f["items"].(map[string]any)
	if !ok {
		return KindAny
	}
	return nestedKind(item["type"])
}

// innerKind is the value shape of one key of a keyed field. Key properties are
// independent copies of one inner field (see Keyed), so any of them will do.
func innerKind(f Field) Kind {
	props, ok := f["properties"].(map[string]any)
	if !ok {
		return KindAny
	}
	for _, name := range sortedKeys(props) {
		prop, ok := props[name].(map[string]any)
		if !ok {
			continue
		}
		return nestedKind(prop["type"])
	}
	return KindAny
}

// nestedKind is the shape of a NESTED field (a list element, one key's value
// of a keyed field), read from its schema half.
//
// COMPOSITE types are as mandatory here as scalars, and not for completeness'
// sake. Without the "array"/"object" branches ui.Keyed(ui.List(ui.String()),
// keys) would collapse to {KindMap, KindAny}, and KindAny in Elem
// short-circuits Match: ANY map cell would accept such a field. A "localized
// tag list" over erjet.KeyedStrings would lint clean, and on Save
// KeyedTable.write parses the key's value as a string - []any gives "", and
// "" there means CLEAR, so every submitted locale would lose its translation.
// The type axis exists against silent write failures; without these two
// branches it would let data loss through.
//
// DEPTH IS ONE LEVEL, and that is a limit of Wire itself, not an omission
// here: it has a single Elem, so ui.List(ui.List(ui.String())) declares
// {KindList, KindList} and the element shape of the INNER list is not declared
// at all - the second level degrades to "not checked". Deliberate: no cell of
// that shape exists in the store, and if one appears it is Wire that must
// grow, not this function.
//
// Assembly markers never get here: the nested field arrives already split
// through Split, which drops $items along with the others. That is why
// ui.Keyed(ui.Items()) is recognised NOT here but by the $keyedItems marker on
// the outer field (see keyedItemsKey and the IsKeyedItems branch in WireKind)
// - the inner field arrives here with no markers.
func nestedKind(raw any) Kind {
	switch {
	case hasType(raw, "string"):
		return KindString
	case hasType(raw, "integer"):
		return KindInteger
	case hasType(raw, "number"):
		return KindNumber
	case hasType(raw, "boolean"):
		return KindBool
	case hasType(raw, "array"):
		return KindList
	case hasType(raw, "object"):
		return KindMap
	}
	return KindAny
}
