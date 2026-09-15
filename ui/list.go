package ui

// List WRAPS any field: an ordered list of its values.
//
// The counterpart of Keyed in structure (the same field, multiplied) but on
// another axis: Keyed has as many values as keys in a closed set, List as
// many as the admin adds. On the wire it is an array: ["a", "b"], and ITS
// ORDER IS SIGNIFICANT.
//
// No custom renderer, unlike Keyed: rjsf handles arrays itself, and the theme
// ships ArrayFieldTemplate with add/remove/move. That is exactly why the
// inner field's ui half must go to uiSchema.items - the only place rjsf takes
// it from (computeItemUiSchema). Our ui:options.inner convention used by
// Keyed would be a dead key here: KeyedField reads it, and it does not draw
// list elements.
//
// The storage counterparts are erjet.Strings/Ints/Floats/Timestamps.
func List(inner Field) Field {
	// Requiredness of the inner field is inexpressible for the same reason as
	// in Keyed: the root "required" is built from form field NAMES, and the
	// element has none. "Non-empty list" is minItems on the field itself, not
	// Required inside.
	if inner.IsRequired() {
		panic("ui.List: inner field is marked Required — use .Min/minItems on the list itself")
	}
	// A nullable ELEMENT is inexpressible: the schema would let null into the
	// array, the cell would refuse to write the whole slice, Table.Assignments
	// would skip the column - and the form would "save" having written
	// nothing. Almost certainly a nullable COLUMN was meant:
	// List(inner).Nullable(), not List(inner.Nullable()).
	if inner.IsNullable() {
		panic("ui.List: inner field is Nullable — did you mean List(inner).Nullable()?")
	}

	innerProp, innerUI := Split(map[string]Field{listItem: inner})

	out := Field{
		"type":  "array",
		"items": innerProp[listItem],
	}
	// No empty ui half: Split creates no empty entries, and an empty items in
	// the uiSchema would add the same noise to goldens.
	if entry, ok := innerUI[listItem]; ok {
		out[itemUIKey] = entry
	}
	return out
}

// listItem is the scratch name used to run the inner field through Split. It
// never leaves: only the halves are taken from the result (cf. keyedInner).
const listItem = "item"
