package ui

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestSplit pins the one rule with meaning in the whole vocabulary: "ui:"
// keys go to the uiSchema, the rest to the JSON Schema property, and a field
// without ui keys gets no uiSchema entry.
func TestSplit(t *testing.T) {
	props, entries := Split(map[string]Field{
		"city":  String().Title("City").Nullable().MaxLen(200),
		"admin": Bool().Title("Admin"),
	})

	wantCity := map[string]any{"type": []string{"string", "null"}, "title": "City", "maxLength": 200}
	if !reflect.DeepEqual(props["city"], wantCity) {
		t.Errorf("props[city] = %v, want %v", props["city"], wantCity)
	}
	if got, want := entries["city"], (map[string]any{"ui:emptyValue": nil}); !reflect.DeepEqual(got, want) {
		t.Errorf("uiEntries[city] = %v, want %v", got, want)
	}
	if got, want := props["admin"], (map[string]any{"type": "boolean", "title": "Admin"}); !reflect.DeepEqual(got, want) {
		t.Errorf("props[admin] = %v, want %v", got, want)
	}
	if _, present := entries["admin"]; present {
		t.Errorf("field without ui: keys must not get a uiSchema entry, got %v", entries["admin"])
	}
}

// TestModifiersCopyOnWrite is the most important test of the type: Field is a
// map, i.e. a reference. Without copy-on-write two branches from one base
// would share state and a stray key would land in a NEIGHBOURING field - a
// test of the "own" field stays green while a foreign one breaks on the client.
func TestModifiersCopyOnWrite(t *testing.T) {
	base := Timestamp().Title("When").Nullable()
	ro := base.Readonly()
	editable := base.Widget("datetime")

	if _, leaked := base["ui:readonly"]; leaked {
		t.Errorf("base mutated by .Readonly(): %v", base)
	}
	if _, leaked := editable["ui:readonly"]; leaked {
		t.Errorf("sibling branch inherited readonly: %v", editable)
	}
	if ro["ui:widget"] != "readonlyDisplay" {
		t.Errorf("ro widget = %v", ro["ui:widget"])
	}
	if editable["ui:widget"] != "datetime" {
		t.Errorf("editable widget = %v", editable["ui:widget"])
	}
}

// TestNullable pins both halves at once: the type in the schema and
// ui:emptyValue in the uiSchema (without it rjsf drops the key from the PATCH
// and clearing the field silently rolls back).
func TestNullable(t *testing.T) {
	f := String().Title("City").Nullable()
	if got, want := f["type"], []string{"string", "null"}; !reflect.DeepEqual(got, want) {
		t.Errorf("type = %v, want %v", got, want)
	}
	if v, ok := f["ui:emptyValue"]; !ok || v != nil {
		t.Errorf("ui:emptyValue = %v (present=%v), want nil/present", v, ok)
	}
}

// TestNullableIsIdempotent: double application is real - a helper already
// called .Nullable() and the call site adds it again. The type must not be
// wrapped twice and the enum must not get a second null.
func TestNullableIsIdempotent(t *testing.T) {
	once := Enum("a", "b").Nullable().EnumLabels(strings.ToUpper)
	twice := once.Nullable()

	if !reflect.DeepEqual(once["type"], twice["type"]) {
		t.Errorf("type drifted: %v -> %v", once["type"], twice["type"])
	}
	wantEnum := []any{nil, "a", "b"}
	if got := twice["enum"]; !reflect.DeepEqual(got, wantEnum) {
		t.Errorf("enum = %v, want %v", got, wantEnum)
	}
	wantNames := []string{UnsetLabel, "A", "B"}
	if got := twice["ui:enumNames"]; !reflect.DeepEqual(got, wantNames) {
		t.Errorf("ui:enumNames = %v, want %v", got, wantNames)
	}
}

// TestReadonlyPicksRendererByField: .Readonly() overrides the constructor's
// widget - a read-only timestamptz is drawn as text, not a datetime input;
// jsonb draws itself through ui:field; a hidden field needs no renderer.
func TestReadonlyPicksRendererByField(t *testing.T) {
	for name, tc := range map[string]struct {
		got  Field
		want map[string]any // only the ui keys under test
	}{
		"scalar":    {String().Title("T").Readonly(), map[string]any{"ui:readonly": true, "ui:widget": "readonlyDisplay"}},
		"timestamp": {Timestamp().Title("T").Readonly(), map[string]any{"ui:readonly": true, "ui:widget": "readonlyDisplay"}},
		"json":      {JSON().Title("T").Readonly(), map[string]any{"ui:readonly": true, "ui:field": "json"}},
		"hidden":    {String().Title("T").Hidden().Readonly(), map[string]any{"ui:readonly": true, "ui:widget": "hidden"}},
	} {
		_, entries := Split(map[string]Field{"f": tc.got})
		if !reflect.DeepEqual(entries["f"], tc.want) {
			t.Errorf("%s: ui entry = %v, want %v", name, entries["f"], tc.want)
		}
	}
}

// TestReadonlyOrderIndependent: .Hidden().Readonly() and .Readonly().Hidden()
// must agree, otherwise chain order becomes a silent trap. Same for Nullable:
// a read-only field has no input, so ui:emptyValue does not belong there.
func TestReadonlyOrderIndependent(t *testing.T) {
	a := String().Title("T").Hidden().Readonly()
	b := String().Title("T").Readonly().Hidden()
	if !reflect.DeepEqual(a, b) {
		t.Errorf("Hidden/Readonly order matters:\n  %v\n  %v", a, b)
	}

	c := Number().Title("T").Nullable().Readonly()
	d := Number().Title("T").Readonly().Nullable()
	if !reflect.DeepEqual(c, d) {
		t.Errorf("Nullable/Readonly order matters:\n  %v\n  %v", c, d)
	}
	if _, dead := c["ui:emptyValue"]; dead {
		t.Errorf("read-only field carries ui:emptyValue (no input to clear): %v", c)
	}
	// The type stays nullable regardless - the column does admit NULL.
	if got, want := c["type"], []string{"number", "null"}; !reflect.DeepEqual(got, want) {
		t.Errorf("type = %v, want %v", got, want)
	}
}

// TestEnumPositional: enum and ui:enumNames are positional. Labels live ONLY
// in the uiSchema: rjsf v6 does not read schema.enumNames (the UI would draw
// String(value), i.e. "null"/"traveler").
func TestEnumPositional(t *testing.T) {
	f := Enum("traveler", "influencer").EnumLabels(func(v string) string { return strings.Title(v) })
	if got, want := f["enum"], []any{"traveler", "influencer"}; !reflect.DeepEqual(got, want) {
		t.Errorf("enum = %v, want %v", got, want)
	}
	if got, want := f["ui:enumNames"], []string{"Traveler", "Influencer"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ui:enumNames = %v, want %v", got, want)
	}
	if _, leaked := f["enumNames"]; leaked {
		t.Error("enumNames must not appear in the schema half — rjsf v6 ignores it there")
	}
	props, _ := Split(map[string]Field{"role": f})
	if _, leaked := props["role"].(map[string]any)["ui:enumNames"]; leaked {
		t.Error("ui:enumNames leaked into the schema half")
	}
}

// TestEscapeHatchesGuardTheHalf: a key in the wrong half is a typo in code,
// not data - it must fail at once rather than quietly land in the other
// document.
func TestEscapeHatchesGuardTheHalf(t *testing.T) {
	for name, call := range map[string]func(){
		"UI with schema key": func() { String().Title("T").UI(map[string]any{"minLength": 1}) },
		"Prop with ui key":   func() { String().Title("T").Prop(map[string]any{"ui:widget": "x"}) },
		"UI with typo'd key": func() { String().Title("T").UI(map[string]any{"uiwidget": "x"}) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("expected a panic")
				}
			}()
			call()
		})
	}

	// Correct usage passes and lands in the right half.
	f := String().Title("T").UI(map[string]any{"ui:autofocus": true}).Prop(map[string]any{"readOnly": true})
	props, entries := Split(map[string]Field{"f": f})
	if entries["f"].(map[string]any)["ui:autofocus"] != true {
		t.Errorf("ui half = %v", entries["f"])
	}
	if props["f"].(map[string]any)["readOnly"] != true {
		t.Errorf("schema half = %v", props["f"])
	}
}

// TestConstructorsAreFresh: a constructor must return a new map, otherwise
// two fields of one kind would share state.
func TestConstructorsAreFresh(t *testing.T) {
	a, b := String().Title("A"), String().Title("B")
	a["x"] = 1
	if _, leaked := b["x"]; leaked {
		t.Error("String() returns a shared map")
	}
}

// TestDateFieldShape: Date() differs from Timestamp() exactly by format -
// "date", not "date-time" - and carries no ui:widget/ui:field, so it renders
// with the stock string input.
func TestDateFieldShape(t *testing.T) {
	f := Date()
	if f["type"] != "string" {
		t.Errorf("type = %v, want \"string\"", f["type"])
	}
	if f["format"] != "date" {
		t.Errorf("format = %v, want \"date\"", f["format"])
	}
	if _, hasWidget := f["ui:widget"]; hasWidget {
		t.Errorf("Date() sets a ui:widget: %v", f["ui:widget"])
	}
}

// TestJSONFieldHasNoType: jsonb arrives as anything, no type is declared; the
// renderer is ui:field because rjsf routes ui:widget only for leaf schemas
// and an object goes to ObjectField.
func TestJSONFieldHasNoType(t *testing.T) {
	props, entries := Split(map[string]Field{"prefs": JSON().Title("Prefs").Readonly()})
	if _, typed := props["prefs"].(map[string]any)["type"]; typed {
		t.Errorf("JSON field declares a type: %v", props["prefs"])
	}
	if entries["prefs"].(map[string]any)["ui:field"] != "json" {
		t.Errorf("ui entry = %v", entries["prefs"])
	}
}

// TestGroupOmitsEmptyFields: on the client title/description/columns/toggleAll
// are optional and columns is the union `1 | 2`. A forgotten omitempty would
// send "columns":0 and "description":"" - the contract broken, and no render
// test would notice: React draws the default.
func TestGroupOmitsEmptyFields(t *testing.T) {
	got, err := json.Marshal(Group{ID: "social", Title: "Social", Fields: []string{"instagram_url"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"id":"social","title":"Social","fields":["instagram_url"]}`; string(got) != want {
		t.Errorf("minimal group = %s, want %s", got, want)
	}

	got, _ = json.Marshal(Group{
		ID: "privacy", Title: "Privacy", Description: "d", Columns: 2, ToggleAll: true,
		Fields: []string{"a", "b"},
	})
	want := `{"id":"privacy","title":"Privacy","description":"d","columns":2,"toggleAll":true,"fields":["a","b"]}`
	if string(got) != want {
		t.Errorf("full group = %s, want %s", got, want)
	}

	// fields WITHOUT omitempty: a section with no fields must arrive as
	// "fields":null, otherwise "nothing to show" is indistinguishable from
	// "forgot to declare".
	got, _ = json.Marshal(Group{ID: "empty"})
	if want := `{"id":"empty","fields":null}`; string(got) != want {
		t.Errorf("empty group = %s, want %s", got, want)
	}
}

// TestHeaderOmitsEmptyFields: the same for Header - the client declares all
// three fields optional and branches on their presence.
func TestHeaderOmitsEmptyFields(t *testing.T) {
	got, _ := json.Marshal(Header{TitleField: "name", SubtitleField: "username", MetaFields: []string{"id"}})
	if want := `{"titleField":"name","subtitleField":"username","metaFields":["id"]}`; string(got) != want {
		t.Errorf("full header = %s, want %s", got, want)
	}
	if got, _ = json.Marshal(Header{TitleField: "name"}); string(got) != `{"titleField":"name"}` {
		t.Errorf("partial header = %s", got)
	}
	if got, _ = json.Marshal(Header{}); string(got) != `{}` {
		t.Errorf("zero header = %s, want {}", got)
	}
}

// TestKeysMirrorTS: the keys must match the client's constants. A drift means
// the client silently fails to find the section.
func TestKeysMirrorTS(t *testing.T) {
	for got, want := range map[string]string{
		GroupsKey: "ui:groups",
		HeaderKey: "ui:header",
	} {
		if got != want {
			t.Errorf("key = %q, want %q", got, want)
		}
	}
}

// TestKeyedSplitsIntoBothHalves: a keyed field leaves in two halves - the key
// set into the schema (with the ban on unknown keys), the switcher into the
// uiSchema. The renderer is declared through ui:field because rjsf does not
// route a widget for an object schema at all.
func TestKeyedSplitsIntoBothHalves(t *testing.T) {
	props, uiEntries := Split(map[string]Field{"bio": Keyed(String(), Keys("en", "ru")).Title("Bio")})

	prop, _ := props["bio"].(map[string]any)
	if prop["type"] != "object" {
		t.Errorf("type = %v, want object", prop["type"])
	}
	if prop["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v, want false — an unknown key must be a 422, not a 500 from Postgres",
			prop["additionalProperties"])
	}
	keys, _ := prop["properties"].(map[string]any)
	if len(keys) != 2 || keys["en"] == nil || keys["ru"] == nil {
		t.Errorf("properties = %v, want en+ru", keys)
	}

	entry, _ := uiEntries["bio"].(map[string]any)
	if entry["ui:field"] != KeyedFieldKey {
		t.Errorf("ui:field = %v, want %q (a widget would be ignored for an object schema)",
			entry["ui:field"], KeyedFieldKey)
	}
	opts, _ := entry["ui:options"].(map[string]any)
	if opts["default"] != "en" {
		t.Errorf("ui:options.default = %v, want the first key", opts["default"])
	}
	// The title stays on the field as a whole, not on one of the keys: one
	// label, as there is one field.
	if prop["title"] != "Bio" {
		t.Errorf("title = %v, want Bio", prop["title"])
	}
}

// TestKeyLabelsKeepTheKeyList: labels are ADDED to ui:options, not replacing
// it. Through the .UI hatch the same call would silently wipe keys and
// default, and the switcher would have no keys in production.
func TestKeyLabelsKeepTheKeyList(t *testing.T) {
	_, uiEntries := Split(map[string]Field{
		"bio": Keyed(String(), Keys("en", "ru")).KeyLabels(func(v string) string { return map[string]string{"en": "English", "ru": "Русский"}[v] }),
	})
	entry, _ := uiEntries["bio"].(map[string]any)
	opts, _ := entry["ui:options"].(map[string]any)

	keys, _ := opts["keys"].([]Key)
	if len(keys) != 2 || opts["default"] != "en" {
		t.Fatalf("ui:options = %v, want keys+default intact", opts)
	}
	if keys[1].Value != "ru" || keys[1].Label != "Русский" {
		t.Errorf("keys[1] = %+v, want ru/Русский — the label lives inside the key", keys[1])
	}

	// Copy-on-write modifier: the original field gets no labels.
	_, plain := Split(map[string]Field{"bio": Keyed(String(), Keys("en", "ru"))})
	plainOpts, _ := plain["bio"].(map[string]any)["ui:options"].(map[string]any)
	plainKeys, _ := plainOpts["keys"].([]Key)
	if plainKeys[0].Label != "" {
		t.Errorf("KeyLabels leaked into a field that never called it: %+v", plainKeys[0])
	}
}

// TestKeyedWrapsAnyField: keyedness is orthogonal to the type - the inner
// field may be anything from the vocabulary, and its halves split as usual:
// the schema half is copied under every key, the uiSchema half travels once
// in ui:options.inner.
//
// Without this a keyed field could hold only bare strings, and a textarea (or
// a relation picker tomorrow) could not go inside.
func TestKeyedWrapsAnyField(t *testing.T) {
	props, uiEntries := Split(map[string]Field{
		"bio":    Keyed(String().Widget("textarea"), Keys("en", "ru")).Title("Bio"),
		"limits": Keyed(Number().Min(0), Keys("free", "pro")),
		"flags":  Keyed(Bool(), Keys("a", "b")),
	})

	bio, _ := props["bio"].(map[string]any)["properties"].(map[string]any)
	if en, _ := bio["en"].(map[string]any); en["type"] != "string" {
		t.Errorf("bio.en = %v, want the inner string schema", bio["en"])
	}
	bioOpts, _ := uiEntries["bio"].(map[string]any)["ui:options"].(map[string]any)
	inner, _ := bioOpts["inner"].(map[string]any)
	if inner["ui:widget"] != "textarea" {
		t.Errorf("ui:options.inner = %v, want the inner ui half", inner)
	}

	// A numeric field takes the same path: the key knows nothing of the type.
	// Its ui half (the number's ui:widget) goes to inner like bio's textarea.
	limits, _ := props["limits"].(map[string]any)["properties"].(map[string]any)
	free, _ := limits["free"].(map[string]any)
	if free["type"] != "number" || free["minimum"] != float64(0) {
		t.Errorf("limits.free = %v, want the inner number schema with minimum", free)
	}
	limitOpts, _ := uiEntries["limits"].(map[string]any)["ui:options"].(map[string]any)
	if limitInner, _ := limitOpts["inner"].(map[string]any); limitInner["ui:widget"] != "number" {
		t.Errorf("ui:options.inner = %v, want the inner number ui half", limitOpts["inner"])
	}

	// A field without ui keys gets no inner entry - an empty object there would be noise.
	flagOpts, _ := uiEntries["flags"].(map[string]any)["ui:options"].(map[string]any)
	if _, present := flagOpts["inner"]; present {
		t.Errorf("ui:options.inner = %v for a field with no ui keys", flagOpts["inner"])
	}

	// Key properties are independent: a map is a reference, and one shared by
	// all would leak an edit of one key into all of them.
	if reflect.ValueOf(bio["en"]).Pointer() == reflect.ValueOf(bio["ru"]).Pointer() {
		t.Error("keys share one schema map — a change to one would leak into all")
	}
}

// TestKeyedRejectsRequiredInner: requiredness of the inner field is
// inexpressible - the root "required" is built from form field names, and
// the inner field has none. A silently swallowed marker would give a field
// required in code and not on the wire.
func TestKeyedRejectsRequiredInner(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected a panic on Required inner field")
		}
	}()
	Keyed(String().Required(), Keys("en"))
}

// TestKeyedLayoutAsksForAFullRow: the expanded layout is N inputs instead of
// one, and in a 50%-wide column they read as a separate section. colSpan is
// set AUTOMATICALLY: the author need not remember the link between layout
// and section markup, and the client's section template already reads it.
func TestKeyedLayoutAsksForAFullRow(t *testing.T) {
	_, entries := Split(map[string]Field{
		"wide":   Keyed(String(), Keys("en", "ru")).KeyedLayout(KeyedExpanded),
		"narrow": Keyed(String(), Keys("en", "ru")).KeyedLayout(KeyedChips),
	})

	wide, _ := entries["wide"].(map[string]any)["ui:options"].(map[string]any)
	if wide["layout"] != KeyedExpanded || wide["colSpan"] != 2 {
		t.Errorf("expanded ui:options = %v, want layout+colSpan", wide)
	}
	// Keys and default intact: the layout is added, not replacing the options.
	if keys, _ := wide["keys"].([]Key); len(keys) != 2 || wide["default"] != "en" {
		t.Errorf("expanded ui:options lost the key list: %v", wide)
	}

	narrow, _ := entries["narrow"].(map[string]any)["ui:options"].(map[string]any)
	if _, wideRow := narrow["colSpan"]; wideRow {
		t.Errorf("chips layout must not claim a full row: %v", narrow)
	}
}

// TestNumberWidgetKey: numeric fields route to our widget (right alignment +
// scale formatting). The counterpart is the client's "number" widget entry.
func TestNumberWidgetKey(t *testing.T) {
	if got := Number()["ui:widget"]; got != "number" {
		t.Errorf("Number() ui:widget = %v, want number", got)
	}
	if got := Integer()["ui:widget"]; got != "number" {
		t.Errorf("Integer() ui:widget = %v, want number", got)
	}
}

// TestDecimalsLivesOnlyInUISchema: precision goes to ui:options.decimals and
// NOT into the schema half - multipleOf on a fractional step makes ajv reject
// falsely on float arithmetic, and the columns are bare numeric anyway.
func TestDecimalsLivesOnlyInUISchema(t *testing.T) {
	f := Number().Decimals(2)
	opts, ok := f["ui:options"].(map[string]any)
	if !ok || opts["decimals"] != 2 {
		t.Fatalf("ui:options = %v, want decimals:2", f["ui:options"])
	}
	if _, leaked := f["multipleOf"]; leaked {
		t.Error("Decimals must not set multipleOf on the schema half")
	}
	props, ui := Split(map[string]Field{"price": f})
	if _, inProp := props["price"].(map[string]any)["ui:options"]; inProp {
		t.Error("ui:options leaked into the schema property")
	}
	entry, _ := ui["price"].(map[string]any)
	eo, _ := entry["ui:options"].(map[string]any)
	if eo["decimals"] != 2 {
		t.Errorf("uiSchema ui:options = %v, want decimals:2", entry["ui:options"])
	}
}

// TestDecimalsMergesWithExistingOptions: Decimals does not wipe previously set
// ui:options (the same copy-on-write as ColSpan).
func TestDecimalsMergesWithExistingOptions(t *testing.T) {
	f := Number().ColSpan(2).Decimals(3)
	opts, _ := f["ui:options"].(map[string]any)
	if opts["colSpan"] != 2 || opts["decimals"] != 3 {
		t.Errorf("ui:options = %v, want colSpan:2 + decimals:3", opts)
	}
}
