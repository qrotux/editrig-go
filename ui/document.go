package ui

import "encoding/json"

// Document is the form as a WHOLE: the field set plus what describes the
// document rather than a field (order, requiredness, sections, header).
// Marshal lays it out into the schema/uiSchema pair Entity.Schema returns.
//
// Why assembly lives here and not in the entity: the entity's own part is
// only the field set, sections and header, while the marshalling policy is
// one for all - which root keys exist at all, that ui:order is mandatory on
// BOTH schemas (without it rjsf takes the order of the incoming object, and
// Go marshals a map alphabetically), that per-field ui entries go to the ROOT
// of the uiSchema under the field name. Copied into every entity it would
// drift - the class of bugs Field prevents at the field level.
//
// Why in ui and not in the core: the core is bound by its import contract
// (doc.go) and assembly needs Field. And it is exactly what ui is declared to
// be (ui.go): form-level concepts, those that describe the whole document.
type Document struct {
	// Fields are the document's fields with both halves at once (see Field);
	// the map key is the schema property name.
	Fields map[string]Field
	// Order lists field names in render order and goes to ui:order. Empty
	// omits the key, and the order falls to the alphabetical map walk (see
	// above why that is unwanted on either schema).
	//
	// It also orders the root "required": requiredness is declared on the
	// fields themselves (Field.Required), and a map holds no order.
	Order []string
	// Groups are the form sections under GroupsKey. Empty omits the key: a
	// short schema needs no sections, and an empty array would be wire noise.
	Groups []Group
	// Header is the record header under HeaderKey. nil omits the key; a zero
	// Header passed by value would arrive as an empty object.
	Header *Header
}

// required is the root "required" array derived from the fields themselves
// (Field.Required). The document deliberately has no separate list of names:
// it would be a second declaration of one fact, and a typo in it would
// require a property the schema lacks - a form that cannot be submitted.
func (d Document) required() []string {
	return requiredOf(d.Fields, d.Order)
}

// Marshal lays the document out into a JSON Schema and a uiSchema.
//
// An error can only come from json.Marshal (Field carries raw values through
// the .Prop/.UI hatches) - the signature repeats Entity.Schema so an entity
// returns the result directly, without repacking.
func (d Document) Marshal() (schema, uiSchema json.RawMessage, err error) {
	props, entries := Split(d.Fields)

	root := map[string]any{"type": "object", "properties": props}
	if req := d.required(); len(req) > 0 {
		root["required"] = req
	}
	schema, err = json.Marshal(root)
	if err != nil {
		return nil, nil, err
	}

	uiRoot := make(map[string]any, len(entries)+3)
	// Field entries live at the ROOT of the uiSchema under the field name -
	// that is where rjsf looks, not in a nested object. Root extensions sit
	// next to them and cannot collide: a property name cannot start with "ui:".
	for name, entry := range entries {
		uiRoot[name] = entry
	}
	if len(d.Order) > 0 {
		// ui:order deliberately has NO constant: it is a key of rjsf itself, not
		// our extension (see ui.go).
		uiRoot["ui:order"] = d.Order
	}
	if len(d.Groups) > 0 {
		uiRoot[GroupsKey] = d.Groups
	}
	if d.Header != nil {
		uiRoot[HeaderKey] = *d.Header
	}
	uiSchema, err = json.Marshal(uiRoot)
	if err != nil {
		return nil, nil, err
	}
	return schema, uiSchema, nil
}
