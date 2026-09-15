package ui

// IconFieldKey is the "ui:field" value of an icon field. A constant for the
// same reason as MediaFieldKey: it mirrors the client's field registry key,
// and each side of the contract holds the name in one place.
const IconFieldKey = "icon"

// Icon is an icon field: a kebab-case Lucide icon NAME on the wire, a
// searchable preview grid in the form.
//
// The cell stays a plain text column - ONLY the presentation changes. There
// is no separate protocol concept and none is needed: the list of names does
// not come from the schema but from the application's own read-only route,
// because it is shared by every icon field and weighs ~216 KB - embedding it
// in every schema would ship it on every form open.
//
// The core deliberately knows NOTHING about the icon set's version: the pin
// lives in the application's icon set, and the entity's Validate, which has
// that import, checks the name.
func Icon() Field {
	return Field{"type": "string", "ui:field": IconFieldKey}
}
