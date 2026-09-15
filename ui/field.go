package ui

import (
	"sort"
	"strings"
)

// Field is one form field as ONE map: keys with the "ui:" prefix go to the
// uiSchema, the rest to the JSON Schema property. The separator comes from
// rjsf's own data, not invented: a property name cannot start with "ui:".
//
// The halves are deliberately inseparable: declared apart (props and uiSchema
// a hundred lines from each other) they produce their own class of bugs - the
// type admits null but ui:emptyValue is forgotten, the key drops out of the
// PATCH, and clearing the field silently rolls back.
//
// Constructors are package functions, modifiers are METHODS. The distinction
// is grammatical, not conventional: ui.Nullable does not exist as a name, and
// .Bool() cannot be written in modifier position. After "ui." completion shows
// only field kinds, after "." only modifiers.
type Field map[string]any

// clone backs the copy-on-write modifiers. Field is a map, i.e. a reference;
// without a copy
//
//	base := ui.Timestamp(t).Nullable()
//	a := base.Readonly()
//	b := base.Widget("datetime")
//
// would give ONE map to all three: b would be readonly, base too, and a test
// of one field would stay green because the stray key landed in a neighbour.
func (f Field) clone() Field {
	out := make(Field, len(f)+1)
	for k, v := range f {
		out[k] = v
	}
	return out
}

func (f Field) with(key string, val any) Field {
	out := f.clone()
	out[key] = val
	return out
}

// --- constructors ----------------------------------------------------------

// Constructors take NO title: it is derived from the field name and set by
// Title when the document is assembled. Otherwise the entry would repeat its
// own name (`"city": ui.String(title("city"))`), and the key and the title
// could drift apart by a typo - silently, with an empty label.

// numberWidget is the "ui:widget" value of numeric fields. The client widget
// right-aligns the number and, when ui:options.decimals is set, formats it to
// N decimal places.
const numberWidget = "number"

// String/Number/Bool are the scalar JSON Schema properties.
func String() Field { return Field{"type": "string"} }
func Number() Field { return Field{"type": "number", "ui:widget": numberWidget} }
func Bool() Field   { return Field{"type": "boolean"} }

// Integer is an integer column (int2/int4/int8). Separate from Number because
// "type": "integer" rejects a fractional VALUE at structural validation, while
// "number" would let it through to the store, where the cell silently
// refuses to write.
func Integer() Field { return Field{"type": "integer", "ui:widget": numberWidget} }

// Timestamp is a timestamptz: an RFC 3339 string on the wire. It imposes no
// editing widget: read-only timestamps render as text, an editable one gets
// .Widget("datetime") at the call site.
func Timestamp() Field {
	return Field{"type": "string", "format": "date-time"}
}

// LocalTimestamp is a `timestamp without time zone` as WALL-CLOCK time: on the
// wire "2006-01-02T15:04:05", no zone and no Z.
//
// format: "date-time" is deliberately NOT declared: it means RFC 3339, which
// requires an offset. Declaring it would lie in the annotation and bring a
// browser hint that gets in the way of typing exactly what is needed.
//
// The widget is custom and set HERE rather than via .Widget() at the call
// site: rjsf's stock "datetime" converts UTC<->local, which is exactly what a
// wall-clock value must not do, and picking it by analogy with Timestamp() is
// too easy. The storage counterpart is erjet.TimeLocal.
func LocalTimestamp() Field {
	return Field{"type": "string", "ui:widget": "localDatetime"}
}

// TimeOfDay is a time of day "15:04": no date, no zone, no seconds.
//
// format: "time" is deliberately NOT declared, and it is not cosmetic: ajv
// treats it as RFC 3339, where seconds are MANDATORY, so a stored "10:30"
// fails validation ALREADY ON FORM LOAD, before the admin touches anything.
//
// The widget is custom and set here for the same reason as in LocalTimestamp:
// rjsf's stock "time" resolves to the core TimeWidget, which appends ":00" on
// every change, i.e. returns a value outside the column's canon. The hour and
// minute range is set at the call site with a pattern.
func TimeOfDay() Field {
	return Field{"type": "string", "ui:widget": "localTime"}
}

// Date is a calendar date, NO time and NO zone: "2006-01-02" on the wire.
//
// Separate from Timestamp() for the same reason LocalTimestamp() is: the
// difference is semantic. Timestamp() is an instant (RFC 3339, with a zone);
// Date() is a whole day, which has no time zone at all, and the format says so
// literally - "format": "date", not "date-time". The storage counterpart is
// erjet.Date.
func Date() Field {
	return Field{"type": "string", "format": "date"}
}

// JSON is a jsonb column. No type is declared (anything may arrive); the
// renderer is ui:FIELD, not widget: rjsf routes a widget only for leaf
// schemas, an object goes to ObjectField. The key is set here, BEFORE any
// modifier, so that .Readonly() sees it and does not override it with its own
// readonlyDisplay.
func JSON() Field {
	return Field{"ui:field": "json"}
}

// KeyedFieldKey is the "ui:field" value of a keyed field. A constant because
// the name mirrors the client's field registry: each side of the contract
// holds it in one place, not as a literal in three.
const KeyedFieldKey = "keyed"

// Key is one key of a keyed field: the value and the switcher label TOGETHER.
//
// A struct rather than two lists: the value and its label are two halves of
// one fact and must not drift apart (the same principle as decl.Entry, where
// the cell sits next to the field). A positional label array next to a value
// array is exactly the construction that forces a lint on enums; here it is
// inexpressible.
type Key struct {
	// Value is the key on the wire: the property name in the object and the row
	// key in storage.
	Value string `json:"value"`
	// Label is the switcher label. Empty means the client shows Value.
	//
	// Usually NOT filled in the declaration: labels are localized and the
	// declaration is static - decl.Set.Fields sets them via KeyLabels, with the
	// same catalog as enum labels.
	Label string `json:"label,omitempty"`
}

// Keys builds keys without labels from bare values, for declarations that
// already hold the list of locales/channels as a string slice.
func Keys(values ...string) []Key {
	out := make([]Key, len(values))
	for i, v := range values {
		out[i] = Key{Value: v}
	}
	return out
}

// Keyed WRAPS any field: the same field, but with its own value for each key
// of a fixed set. Text per locale, limit per plan, flag per channel - what a
// key means is known only to the entity that owns the field, which is why the
// word "locale" does not appear here.
//
// Keyedness is ORTHOGONAL to the type: the inner field may be anything from
// this vocabulary (String().Widget("textarea"), Number().Min(0), Enum(...)),
// and its halves split as usual - the schema half is copied under every key,
// the uiSchema half travels once in ui:options.inner. One copy, because the
// field is the same for every key: different widgets for different locales
// are already different fields.
//
// On the wire it is ONE object `{"en": "...", "ru": "..."}`, not a field per
// key: otherwise the form would get five entries instead of one - five
// titles, five places in ui:order, five rows in the section. Visually it
// differs from a plain field only by the key switcher in the label. The
// storage counterpart is erjet.KeyedStrings, and the boundary is the same.
//
// The renderer is ui:field, NOT ui:widget: rjsf routes a widget only for leaf
// schemas (string/number/boolean), an object goes to ObjectField and ignores
// the widget - the same reason as in JSON above. The renderer draws the nested
// SchemaField of the active key, so any widget works inside without a change.
//
// Which key is being edited is form state, not data, so the switcher lives on
// the client side and never leaks into the schema. Go emits only the LIST of
// keys and the default (ui:options) and stays their source of truth.
//
// additionalProperties:false is deliberate: an unknown key in the payload must
// become a 422 from structural validation, not reach the store - there it
// would go into an enum column and come back as a 500 from Postgres.
func Keyed(inner Field, keys []Key) Field {
	// Requiredness of the inner field is inexpressible: the root "required" is
	// built from form field NAMES, and the inner field has none. Swallowing the
	// marker silently would give a field that looks required in code and is not
	// on the wire. Panic, as .Prop does with the same marker.
	if inner.IsRequired() {
		panic("ui.Keyed: inner field is marked Required — mark the keyed field itself instead")
	}

	innerProp, innerUI := Split(map[string]Field{keyedInner: inner})
	props := make(map[string]any, len(keys))
	for _, k := range keys {
		// A copy per key: maps are references, and the key properties must be
		// independent (otherwise editing one would leak into all).
		prop := make(map[string]any, len(innerProp[keyedInner].(map[string]any)))
		for pk, pv := range innerProp[keyedInner].(map[string]any) {
			prop[pk] = pv
		}
		props[k.Value] = prop
	}

	options := map[string]any{"keys": append([]Key(nil), keys...)}
	if len(keys) > 0 {
		options["default"] = keys[0].Value
	}
	if entry, ok := innerUI[keyedInner]; ok {
		options["inner"] = entry
	}

	out := Field{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
		"ui:field":             KeyedFieldKey,
		"ui:options":           options,
	}
	if inner.IsItems() {
		// The flag is read off inner BEFORE Split (which drops $items): from it
		// decl.Set.Fields picks WithKeyedItems and WireKind picks KindItems.
		out[keyedItemsKey] = true
	}
	return out
}

// keyedInner is the scratch name used to run the inner field through Split.
// It never leaves: only the halves are taken from the result.
const keyedInner = "inner"

// Layouts of the key switcher. The ENTITY AUTHOR picks: how many keys the
// field has and how equal they are is known there, not in the library.
//
//   - KeyedPopover (default) - a link labelled with the active key, a menu on
//     click. Compact and does not grow with the number of keys.
//   - KeyedChips - a row of buttons: every key visible, but with a dozen keys
//     the row outweighs the input itself.
//   - KeyedExpanded - no switcher, every key shown at once, one field per key.
//     Fits two or three keys that are edited together.
const (
	KeyedPopover  = "popover"
	KeyedChips    = "chips"
	KeyedExpanded = "expanded"
)

// KeyedLayout sets the switcher layout (see the constants above).
//
// KeyedExpanded additionally asks for a full section row: an expanded field is
// N inputs, and in a 50%-wide column they read as a separate section. The
// colSpan key is set here so the author need not remember that link.
func (f Field) KeyedLayout(layout string) Field {
	opts, _ := f["ui:options"].(map[string]any)
	next := make(map[string]any, len(opts)+2)
	for k, v := range opts {
		next[k] = v
	}
	next["layout"] = layout
	if layout == KeyedExpanded {
		next["colSpan"] = 2
	}
	return f.with("ui:options", next)
}

// ColSpan sets how many section columns the field occupies (1 or 2). A field
// that draws an editor rather than an input is unreadable at half width.
func (f Field) ColSpan(n int) Field {
	opts, _ := f["ui:options"].(map[string]any)
	next := make(map[string]any, len(opts)+1)
	for k, v := range opts {
		next[k] = v
	}
	next["colSpan"] = n
	return f.with("ui:options", next)
}

// Decimals sets the number of decimal places of a numeric field. It lives
// ONLY in the uiSchema (ui:options.decimals): the widget derives step=10^-n
// from it and formats the value to n places on blur. Precision does NOT go
// into the JSON Schema: multipleOf on a fractional step gives false ajv errors
// from float arithmetic (0.29 % 0.01 != 0), and the columns are bare numeric
// anyway, so there is nothing to bound the multiple by.
func (f Field) Decimals(n int) Field {
	opts, _ := f["ui:options"].(map[string]any)
	next := make(map[string]any, len(opts)+1)
	for k, v := range opts {
		next[k] = v
	}
	next["decimals"] = n
	return f.with("ui:options", next)
}

// KeyLabels sets labels on the DECLARED keys ("en" -> "English").
//
// Same signature and role as EnumLabels: labels are localized, i.e. depend on
// the request, while the key set does not, so the declaration holds bare keys
// and document assembly (decl.Set.Fields) adds labels from the same catalog.
// A label for a key that does not exist is inexpressible here - only existing
// keys are filled, so no lint has to check that pair.
func (f Field) KeyLabels(label func(value string) string) Field {
	opts, _ := f["ui:options"].(map[string]any)
	keys, ok := opts["keys"].([]Key)
	if !ok {
		return f
	}
	labelled := make([]Key, len(keys))
	for i, k := range keys {
		k.Label = label(k.Value)
		labelled[i] = k
	}
	next := make(map[string]any, len(opts))
	for k, v := range opts {
		next[k] = v
	}
	next["keys"] = labelled
	return f.with("ui:options", next)
}

// IsKeyed reports whether the field is a keyed field (see Keyed). Exported for
// document assembly, which sets key labels exactly like enum labels and needs
// the flag from outside.
func (f Field) IsKeyed() bool { return f["ui:field"] == KeyedFieldKey }

// Enum is an enumeration. Labels arrive READY: the mapping "value -> catalog
// key" is the entity's convention, the vocabulary knows nothing of catalogs.
//
// enum and ui:enumNames are positional and must stay in step. Labels live in
// the uiSchema on purpose: rjsf v6 reads them ONLY from there (@rjsf/utils
// optionsList.js looks at getUiOptions(uiSchema).enumNames and never at
// schema.enumNames), so "enumNames" at schema level would be dead data and the
// UI would draw String(value), i.e. "null"/"traveler".
func Enum(values ...string) Field {
	vals := make([]any, len(values))
	for i, v := range values {
		vals[i] = v
	}
	return Field{"type": "string", "enum": vals}
}

// EnumLabels builds "ui:enumNames" positionally from the current "enum": label
// gives the label of each value, the null element gets UnsetLabel.
//
// The array is positional and must stay in step with "enum", hence it is built
// from it rather than from a separate list. Why labels live in the uiSchema
// and not the schema - see Enum above.
func (f Field) EnumLabels(label func(value string) string) Field {
	vals, ok := f["enum"].([]any)
	if !ok {
		return f
	}
	names := make([]string, len(vals))
	for i, v := range vals {
		s, isStr := v.(string)
		if !isStr {
			names[i] = UnsetLabel // the null element reads "not set"
			continue
		}
		names[i] = label(s)
	}
	return f.with("ui:enumNames", names)
}

// --- modifiers: JSON Schema ------------------------------------------------

// Title sets the localized field label. Document assembly derives it from the
// field name instead of writing it by hand in every entry.
func (f Field) Title(s string) Field { return f.with("title", s) }

func (f Field) Min(v float64) Field     { return f.with("minimum", v) }
func (f Field) Max(v float64) Field     { return f.with("maximum", v) }
func (f Field) MinLen(n int) Field      { return f.with("minLength", n) }
func (f Field) MaxLen(n int) Field      { return f.with("maxLength", n) }
func (f Field) Pattern(re string) Field { return f.with("pattern", re) }
func (f Field) Format(s string) Field   { return f.with("format", s) }
func (f Field) Default(v any) Field     { return f.with("default", v) }

// MaxItems caps the array LENGTH of wrapper fields (List, Items) and multi
// relations (Relation, MediaMulti). It goes on the array field itself; for
// ui.Keyed(ui.List(...)) on the inner List BEFORE wrapping: the outer type is
// object and the arrays sit in each key's property (Keyed copies them along
// with every schema key of the inner field).
func (f Field) MaxItems(n int) Field { return f.with("maxItems", n) }

// --- modifiers: uiSchema ---------------------------------------------------

func (f Field) Widget(s string) Field      { return f.with("ui:widget", s) }
func (f Field) Help(s string) Field        { return f.with("ui:help", s) }
func (f Field) Placeholder(s string) Field { return f.with("ui:placeholder", s) }
func (f Field) Disabled() Field            { return f.with("ui:disabled", true) }

// Hidden mounts the field without showing it. Hiding, not removal from the
// schema: rjsf keeps hidden fields in the render tree with their subtree and
// errors, and the Header reads their values from the data.
func (f Field) Hidden() Field { return f.with("ui:widget", "hidden") }

// --- modifiers: neither half -----------------------------------------------

// requiredKey is the required-field marker.
//
// It lives INSIDE Field because requiredness is declared where the field is -
// but on the wire it is in neither half: in JSON Schema "required" is an ARRAY
// on the parent object, not a property key ("required": true is draft-04
// syntax, the 2020-12 compiler rejects such a schema). Document.Marshal builds
// the root array, Split drops the marker, Lint ignores it, .Prop refuses it
// written by hand. TestRequiredNeverReachesTheWire guards that it never
// reaches the wire.
//
// The "$" prefix is taken because neither a property name nor a uiSchema key
// starts with it - a third namespace inside the map, next to "ui:". The word
// itself is deliberately NOT one of the reserved 2020-12 core keys
// ($id/$ref/$defs/...): the marker is not one of them and must not read as one.
const requiredKey = "$required"

// itemUIKey is an assembly marker: the ui half of a list ELEMENT (see List).
//
// A third namespace inside the map, next to requiredKey and for the same
// reason: the key is not on the wire - it goes into the uiSchema entry under
// the name "items", because rjsf takes an array element's uiSchema ONLY from
// there (@rjsf/core computeItemUiSchema). The name "items" is not available in
// the Field map: the schema half already owns it, and Split routes keys by
// the "ui:" prefix, so there is no other way to say "items, but in the other
// half".
//
// Not "$items": that would differ from the schema "items" by one character,
// and both sit in one map.
const itemUIKey = "$itemUI"

// keyedItemsKey marks "a keyed field whose value is a list of OBJECTS" (the
// counterpart of erjet.KeyedChildRows). Keyed sets it on the OUTER field when
// the inner one is Items: the inner $items dies in Split while Keyed is built,
// and WireKind checks IsItems() as its FIRST branch - reusing $items on the
// outer field would make the keyed field read as a list. Fourth in the third
// namespace with the same fate: it never reaches the wire (Split's drop list);
// its readers (WireKind, decl.Set.Fields, both lints) work on the declaration
// field before the top-level Split.
const keyedItemsKey = "$keyedItems"

// Required marks the field required (it goes into the schema's root
// "required").
//
// A required field must be writable: the engine strips read-only keys BEFORE
// structural validation (validate.StripReadonly), so .Required().Readonly()
// would give "missing property" on EVERY submit - the form could not be sent
// at all, and the cause would look like a client error. The entity gate
// (TestRequiredFieldsAreWritable) checks this.
func (f Field) Required() Field { return f.with(requiredKey, true) }

// IsRequired reports whether the field is declared required. Exported because
// the flag is needed outside too: the entity declaration derives the create
// form from it, not from a second list of names.
func (f Field) IsRequired() bool {
	b, _ := f[requiredKey].(bool)
	return b
}

// --- modifiers: both halves ------------------------------------------------

// Nullable marks the column as admitting NULL. The only modifier that writes
// to BOTH documents: the type in the schema and ui:emptyValue in the uiSchema.
//
// Why ui:emptyValue is mandatory: rjsf's BaseInputTemplate returns
// options.emptyValue when the input is cleared; the default (undefined) DROPS
// the key from the PATCH payload, and Save treats an absent key as "leave the
// column alone", so clearing a nullable field silently rolls back.
//
// Idempotent: applying it twice (a helper already called .Nullable() and the
// call site adds it again) must neither wrap the type a second time nor add a
// second null to the enum.
func (f Field) Nullable() Field {
	if f.isNullable() {
		return f
	}
	out := f.clone()
	if t, ok := out["type"].(string); ok {
		out["type"] = []string{t, "null"}
	}
	// A read-only field has no input - nothing to clear, and ui:emptyValue would
	// be a dead key on the wire. Symmetric with Readonly() below, so that
	// .Nullable().Readonly() and .Readonly().Nullable() give the same result.
	if ro, _ := out["ui:readonly"].(bool); !ro {
		out["ui:emptyValue"] = nil
	}
	// For an enumeration null must also enter the enum itself: even when "type"
	// admits null, ajv/jsonschema still requires a null instance in the value
	// list. Labels (ui:enumNames) are built later from this same list - see
	// EnumLabels. A column without DEFAULT really is NULL right after create,
	// and without this branch the first submit of such a row would be rejected
	// by structural validation.
	if vals, ok := out["enum"].([]any); ok {
		out["enum"] = append([]any{nil}, vals...)
	}
	return out
}

// IsNullable reports whether the field is declared as admitting NULL (see
// Nullable). Exported for the same reason as IsReadonly/IsHidden/IsRequired:
// declaration gates must read the flag from WHERE it is declared, not from a
// third, intermediate list. Its first consumer is ui.List, which has to tell a
// nullable ELEMENT (inexpressible) from a nullable LIST (the ordinary case).
func (f Field) IsNullable() bool { return f.isNullable() }

func (f Field) isNullable() bool {
	t, ok := f["type"].([]string)
	if !ok {
		return false
	}
	for _, x := range t {
		if x == "null" {
			return true
		}
	}
	return false
}

// Readonly marks the field as not accepted for writing: the engine strips it
// from the payload before Validate/Save (validate.StripReadonly looks for
// exactly ui:readonly), so a forged or stale form cannot overwrite
// worker-owned values.
//
// The renderer overrides the constructor's widget: a read-only timestamptz is
// drawn as text, not a datetime input. A hidden field needs no renderer, and
// jsonb draws itself through ui:field - both branches below. Thanks to them
// .Hidden().Readonly() and .Readonly().Hidden() give the same result.
func (f Field) Readonly() Field {
	out := f.with("ui:readonly", true)
	// see Nullable: emptyValue is about clearing an input, and there is none here
	delete(out, "ui:emptyValue")
	if out["ui:widget"] == "hidden" {
		return out
	}
	if _, drawnAsField := out["ui:field"]; drawnAsField {
		return out
	}
	out["ui:widget"] = "readonlyDisplay"
	return out
}

// --- predicates ------------------------------------------------------------

// IsReadonly reports the declared read-only flag, read from the same key that
// goes to the wire. Exported for the declaration gates (decl.Lint): they
// compare presentation with persistence and must read the flag from WHERE it
// is declared, not from a third, intermediate list - the drift of such a list
// from both sides is exactly the defect this prevents.
func (f Field) IsReadonly() bool {
	b, _ := f["ui:readonly"].(bool)
	return b
}

// IsHidden reports whether the field is mounted but not shown (see Hidden).
func (f Field) IsHidden() bool { return f["ui:widget"] == "hidden" }

// --- escape hatches --------------------------------------------------------

// UI and Prop add raw keys for the long tail of rjsf that is not worth
// modelling as methods.
//
// Panic rather than a silent write: a key in the wrong half is a typo in code
// (keys here must be literals), not user data, and it must fail on the first
// test run. The schema is built per request, so reachability of the panic
// from a handler is the price of this check; dynamically built keys do not
// belong here.
func (f Field) UI(kv map[string]any) Field {
	out := f.clone()
	for k, v := range kv {
		if !strings.HasPrefix(k, uiPrefix) {
			panic("ui.Field.UI: key " + k + " is not a uiSchema key — use .Prop")
		}
		out[k] = v
	}
	return out
}

func (f Field) Prop(kv map[string]any) Field {
	out := f.clone()
	for k, v := range kv {
		if strings.HasPrefix(k, uiPrefix) {
			panic("ui.Field.Prop: key " + k + " is a uiSchema key — use .UI")
		}
		// The required marker is not a schema property: written by hand it would
		// bypass Document.Marshal and do nothing. (.UI already panics on it - the
		// key does not start with "ui:".)
		if k == requiredKey {
			panic("ui.Field.Prop: " + requiredKey + " is not a schema keyword — use .Required()")
		}
		if k == itemUIKey {
			panic("ui.Field.Prop: " + itemUIKey + " is not a schema keyword — use ui.List")
		}
		if k == itemsKey {
			panic("ui.Field.Prop: " + itemsKey + " is not a schema keyword — use ui.Items")
		}
		if k == keyedItemsKey {
			panic("ui.Field.Prop: " + keyedItemsKey + " is not a schema keyword — use ui.Keyed(ui.Items(), keys)")
		}
		out[k] = v
	}
	return out
}

const uiPrefix = "ui:"

// --- splitting -------------------------------------------------------------

// Split lays the fields out over the two rjsf documents: keys with the "ui:"
// prefix go to the uiSchema, the rest to the JSON Schema properties. The only
// place in the whole vocabulary with logic in it.
//
// The required marker (requiredKey) goes NOWHERE: its place is the root
// "required" array, which Document.Marshal builds. itemsKey and keyedItemsKey
// are markers of the same class: they never reach the wire.
//
// A field without a single ui key gets no uiSchema entry - an empty object
// there is useless and would only add noise to goldens.
func Split(fields map[string]Field) (props, uiEntries map[string]any) {
	props = make(map[string]any, len(fields))
	uiEntries = make(map[string]any, len(fields))
	for name, f := range fields {
		prop := make(map[string]any, len(f))
		entry := make(map[string]any, len(f))
		for k, v := range f {
			if k == requiredKey || k == itemsKey || k == keyedItemsKey {
				continue
			}
			if k == itemUIKey {
				// The only key that changes its name on the wire: in the ui entry
				// it must be called "items" (see itemUIKey).
				entry["items"] = v
				continue
			}
			if strings.HasPrefix(k, uiPrefix) {
				entry[k] = v
			} else {
				prop[k] = v
			}
		}
		props[name] = prop
		if len(entry) > 0 {
			uiEntries[name] = entry
		}
	}
	return props, uiEntries
}

// requiredOf derives an object's "required" array from the fields themselves.
//
// Shared by the root document and the element of an object list: the rule is
// one ("requiredness is declared on the field, order comes from Order, a
// required field missing from Order is appended alphabetically"), and written
// twice it would drift - at the top level that gives a form that cannot be
// submitted, inside an element the same thing but less visibly.
func requiredOf(fields map[string]Field, order []string) []string {
	out := make([]string, 0, 4)
	ordered := make(map[string]bool, len(order))
	for _, name := range order {
		ordered[name] = true
		if fields[name].IsRequired() {
			out = append(out, name)
		}
	}
	var stray []string
	for name, f := range fields {
		if !ordered[name] && f.IsRequired() {
			stray = append(stray, name)
		}
	}
	sort.Strings(stray)
	return append(out, stray...)
}
