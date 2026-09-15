// Package ui is the uiSchema vocabulary of the editor: the keys and fragments
// that entities assemble into their uiSchema and that the engine and the
// client library read.
//
// Why a separate package rather than a file in the core:
//
//   - Every fragment here has a PAIRED renderer on the client side. The
//     contract spans a language boundary and both halves must change
//     together, so it needs one home, not "a bit in the engine, a bit in the
//     entity".
//   - Names stop stuttering: ui.String(...) instead of editrig.StringPropUI().
//
// The unit of the vocabulary is Field (field.go): one field with BOTH halves
// at once, schema and uiSchema. Only form-level concepts stay here - keys,
// sections and the header, i.e. what describes the document as a whole
// rather than a field.
//
// The package is a LEAF: it imports nothing, the core included.
package ui

// GroupsKey and HeaderKey are the root keys of the declarative uiSchema
// extensions (Group / Header below). They mirror the client's constants: both
// sides of the contract hold the key as a constant, not a literal.
//
// `ui:order` deliberately has NO constant: it is not our extension but a key
// of rjsf itself, and we do not define its semantics.
const (
	GroupsKey = "ui:groups"
	HeaderKey = "ui:header"
)

// UnsetLabel is the "no value" label of a nullable enum. A language-neutral
// glyph rather than a catalog key.
const UnsetLabel = "—"

// Group is one form section under GroupsKey. The client's counterpart has
// optional fields, hence omitempty here: an empty value must DISAPPEAR from
// the JSON, not arrive as zero. Columns especially - on the client it is the
// union `1 | 2`, and "columns": 0 would break the declared contract (Go's type
// system cannot express that union, so the entity's test guards the range).
//
// Title/Description arrive ALREADY localized: Go holds the domain copy, the
// client library owns only the chrome.
//
// Fields named by no section are drawn by the client in an implicit trailing
// one - an added column degrades to "shown without a group", not lost.
type Group struct {
	// ID is the stable section key (React key / test hook).
	ID string `json:"id"`
	// Title and Description are the localized section labels.
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// Columns is the field layout inside the section: 1 or 2. Zero omits the
	// key and the client takes its default (1).
	Columns int `json:"columns,omitempty"`
	// ToggleAll shows "select all/none" for the section's boolean fields.
	ToggleAll bool `json:"toggleAll,omitempty"`
	// Fields are the section's field names in render order.
	Fields []string `json:"fields"`
}

// Header is the record header spec under HeaderKey: which data fields to lift
// from the form body to the top. All fields are optional on the client side.
//
// Meant for identity fields plus row bookkeeping (id/created_at/updated_at),
// which the form itself hides via Field.Hidden().
type Header struct {
	TitleField    string   `json:"titleField,omitempty"`
	SubtitleField string   `json:"subtitleField,omitempty"`
	MetaFields    []string `json:"metaFields,omitempty"`
}
