package ui

import (
	"fmt"
	"sort"
	"strings"
)

// keywordsByType lists which JSON Schema keywords are meaningful for which
// type. Source: draft 2020-12 "Validation" 6.2 (numeric), 6.3 (string):
// length and pattern apply only to strings, minimum/maximum only to numbers.
var keywordsByType = map[string]map[string]bool{
	"string":  {"minLength": true, "maxLength": true, "pattern": true, "format": true},
	"number":  {"minimum": true, "maximum": true, "exclusiveMinimum": true, "exclusiveMaximum": true, "multipleOf": true},
	"integer": {"minimum": true, "maximum": true, "exclusiveMinimum": true, "exclusiveMaximum": true, "multipleOf": true},
	"boolean": {},
	// object is the keyed field (Keyed): the key set and the ban on extras.
	// Length and pattern do not apply to it any more than minimum to a string.
	"object": {"properties": true, "additionalProperties": true},
	// array is a multi relation (Relation): element shape and no duplicates.
	// maxLength does not belong here - array length is bounded by maxItems,
	// and without this row the lint would accept the first and reject the
	// second.
	"array": {"items": true, "uniqueItems": true, "minItems": true, "maxItems": true},
}

// keyedLayouts are the key switcher layouts the client understands. The list
// is closed: an unknown value falls back to the default there silently.
var keyedLayouts = map[string]bool{
	KeyedPopover: true, KeyedChips: true, KeyedExpanded: true,
}

// typeAgnostic are the keys allowed with any type.
var typeAgnostic = map[string]bool{
	"type": true, "title": true, "description": true,
	"enum": true, "default": true, "const": true, "readOnly": true, "writeOnly": true,
}

// Lint checks that every field's JSON Schema keywords are meaningful for its
// type and returns the list of findings (empty means clean).
//
// Why: Field distinguishes constructors from modifiers grammatically but does
// NOT check that a modifier fits the type - ui.Bool().MaxLen(50) compiles and
// reaches the client with maxLength on a boolean property. Neither ajv nor
// santhosh-tekuri complains: per spec a keyword inapplicable to the instance
// type is simply ignored. The mistake shows up NOWHERE, so only a lint like
// this can catch it.
//
// Called from the entity's test over its field map, not at runtime: it is a
// declaration defect, always present in the code or always absent.
//
// A field without "type" (jsonb - anything may arrive) is checked only for
// type-agnostic keys: there is nothing to narrow it with.
//
// Recurses into wrappers: the List element, Keyed values, Items subfields -
// otherwise List(Bool().MaxLen(50)) would reach the client with maxLength on
// a boolean property and the lint would stay silent. The message carries a
// path ("itinerary.day"), not a bare field name - a finding inside a wrapper
// is unreadable otherwise: which top-level field to look under.
func Lint(fields map[string]Field) []string {
	return lintFields("", fields)
}

func lintFields(prefix string, fields map[string]Field) []string {
	var problems []string
	for _, name := range sortedKeys(fields) {
		f := fields[name]
		allowed, typed := allowedKeywords(f["type"])
		for _, key := range sortedKeys(f) {
			// requiredKey, itemUIKey, itemsKey and keyedItemsKey are assembly
			// markers, not schema keywords: none reaches the wire as itself (see
			// Split), so there is no type applicability to check.
			if strings.HasPrefix(key, uiPrefix) || typeAgnostic[key] ||
				key == requiredKey || key == itemUIKey || key == itemsKey || key == keyedItemsKey {
				continue
			}
			if !typed {
				problems = append(problems, fmt.Sprintf(
					"%s: keyword %q on an untyped field — nothing constrains it", fieldPath(prefix, name), key))
				continue
			}
			if !allowed[key] {
				problems = append(problems, fmt.Sprintf(
					"%s: keyword %q does not apply to type %v — JSON Schema ignores it silently",
					fieldPath(prefix, name), key, f["type"]))
			}
		}
		problems = append(problems, lintKeyed(fieldPath(prefix, name), f)...)
		problems = append(problems, lintRelation(fieldPath(prefix, name), f)...)
		problems = append(problems, lintMedia(fieldPath(prefix, name), f)...)
		problems = append(problems, lintIconEnum(fieldPath(prefix, name), f)...)
		problems = append(problems, lintNested(fieldPath(prefix, name), f)...)
	}
	return problems
}

// fieldPath is the field name with nesting: "itinerary.day", "bio.en",
// "tags.items". A human reads the lint message, and "keyword "maxLength" does
// not apply" without a path sends them searching the whole declaration.
//
// Not "path": that is a standard library package name, and at package level
// it would block a future `import "path"`.
func fieldPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// lintNested descends into wrappers: the list element, each key's value of a
// keyed field, the subfields of an object list element.
//
// The halves are rebuilt from the schema part with the ui part mixed in from
// $itemUI/ui:options.inner: the lint needs the JOINED view of a field - what
// it checks lives in both halves (the type in the schema one, ui:field in the
// ui one).
func lintNested(name string, f Field) []string {
	switch {
	case f.IsItems():
		item, _ := f["items"].(map[string]any)
		props, _ := item["properties"].(map[string]any)
		if len(props) == 0 {
			// Fires both on bare ui.Items() (item is nil, props too) and on
			// WithItems(nil, nil) (item exists but properties is {}): the form
			// draws empty cards without a single input either way, so it is one
			// declaration defect, not two.
			return []string{fmt.Sprintf(
				"%s: items field without an element declaration — the form would render empty cards; "+
					"did you forget decl.Entry.Items?", name)}
		}
		entry, _ := f[itemUIKey].(map[string]any)
		return lintFields(name, rejoin(props, entry))
	case f.IsKeyedItems():
		// Its own branch rather than the generic keyed one: the merged per-key
		// field would go to the array branch, and the item schema would be
		// linted as an object-typed field with a "required" key, which
		// keywordsByType does not allow for object - a false finding on every
		// required subfield.
		props, _ := f["properties"].(map[string]any)
		opts, _ := f["ui:options"].(map[string]any)
		inner, _ := opts["inner"].(map[string]any)
		// The items schema copies are identical for every key by construction
		// (WithKeyedItems) - the first key is linted, just as innerKind reads
		// the first property.
		for _, key := range sortedKeys(props) {
			prop, _ := props[key].(map[string]any)
			item, _ := prop["items"].(map[string]any)
			subProps, _ := item["properties"].(map[string]any)
			if len(subProps) == 0 {
				return []string{fmt.Sprintf(
					"%s: keyed items field without an element declaration — the form would render empty cards; "+
						"did you forget decl.Entry.Items?", name)}
			}
			entry, _ := inner["items"].(map[string]any)
			return lintFields(name, rejoin(subProps, entry))
		}
		return nil // no keys is already a lintKeyed finding
	case f.IsKeyed():
		props, _ := f["properties"].(map[string]any)
		opts, _ := f["ui:options"].(map[string]any)
		inner, _ := opts["inner"].(map[string]any)
		fields := make(map[string]Field, len(props))
		for key, raw := range props {
			prop, _ := raw.(map[string]any)
			fields[key] = mergeHalves(prop, inner)
		}
		return lintFields(name, fields)
	case hasType(f["type"], "array") && f["items"] != nil:
		item, _ := f["items"].(map[string]any)
		entry, _ := f[itemUIKey].(map[string]any)
		return lintFields(name, map[string]Field{"items": mergeHalves(item, entry)})
	}
	return nil
}

// rejoin glues the halves of an element's subfields back into Fields: the
// schema ones sit in items.properties, the ui ones in $itemUI under the same
// name.
func rejoin(props, entry map[string]any) map[string]Field {
	out := make(map[string]Field, len(props))
	for name, raw := range props {
		prop, _ := raw.(map[string]any)
		sub, _ := entry[name].(map[string]any)
		out[name] = mergeHalves(prop, sub)
	}
	return out
}

// mergeHalves glues the schema and ui halves of ONE field into a joined Field
// - the view the lint can check (the type lives in the schema half, ui:field
// in the ui half, and apart neither check works).
//
// "items" is special: Split renames $itemUI to "items" in the ui half (see
// itemUIKey), so for a field that is ITSELF a wrapper (a List inside an Items
// element, or a Keyed value that is a List) "items" is present in BOTH halves
// - the schema element on one side, its ui half on the other. A flat
// assignment (last key wins) would silently wipe the schema element with the
// ui half, and the defect the recursion exists for (maxLength on a boolean
// field) would stop showing at the second nesting level. Instead "items" is
// merged ONE MORE level down with this same function; for a scalar element
// (no "items" of its own) the recursion stops at once. The result is stored
// as map[string]any, not Field, because lintNested's array branch expects
// "items" as map[string]any.
func mergeHalves(prop, uiHalf map[string]any) Field {
	f := make(Field, len(prop)+len(uiHalf))
	for k, v := range prop {
		f[k] = v
	}
	for k, v := range uiHalf {
		if k == "items" {
			if schemaItems, ok := f["items"].(map[string]any); ok {
				if uiItems, ok2 := v.(map[string]any); ok2 {
					f["items"] = map[string]any(mergeHalves(schemaItems, uiItems))
					continue
				}
			}
		}
		f[k] = v
	}
	return f
}

// lintRelation checks that the two halves of a relation field agree.
//
// Cardinality is declared TWICE: by the schema type (validator) and by the
// multi flag (widget). Drifted apart, they give a form that sends an array
// into a string property and gets a 422 out of nowhere - and it looks like a
// client error.
//
// The collection is mandatory: without it the widget has nowhere to take
// options from and shows an empty list silently - no error anywhere.
func lintRelation(name string, f Field) []string {
	if !f.IsRelation() {
		return nil
	}
	opts, _ := f["ui:options"].(map[string]any)

	var problems []string
	if collection, _ := opts["collection"].(string); collection == "" {
		problems = append(problems, fmt.Sprintf(
			"%s: relation field without a collection — the picker would have nothing to search", name))
	}

	multi, _ := opts["multi"].(bool)
	isArray := hasType(f["type"], "array")
	isString := hasType(f["type"], "string")
	switch {
	case multi && !isArray:
		problems = append(problems, fmt.Sprintf(
			"%s: multi relation must be an array — the widget sends a list, the validator expects %v", name, f["type"]))
	case !multi && !isString:
		problems = append(problems, fmt.Sprintf(
			"%s: single relation must be a string — the widget sends one id, the validator expects %v", name, f["type"]))
	}
	return problems
}

// lintMedia checks aspect against the closed MediaAspects list.
//
// Collection and cardinality of media are already checked by lintRelation
// (IsRelation is true for both relation-like fields) - what remains is what
// Relation lacks: aspect becomes a CSS class on the client, and a typo in it
// would otherwise fall back to the default silently, visible only by eye on
// a live form.
func lintMedia(name string, f Field) []string {
	if f["ui:field"] != MediaFieldKey {
		return nil
	}
	opts, _ := f["ui:options"].(map[string]any)
	aspect, _ := opts["aspect"].(string)
	if aspect == "" {
		return []string{fmt.Sprintf(
			"%s: media field without an aspect — the widget would not know what shape to render", name)}
	}
	if !MediaAspects[aspect] {
		return []string{fmt.Sprintf(
			"%s: media field aspect %q is not one of the known ratios — the TS side would silently fall back to a default",
			name, aspect)}
	}
	return nil
}

// lintIconEnum checks previews against the enum values: a value without a URL
// would draw an empty tile with no error anywhere - the same class of silent
// defect as aspect on media.
func lintIconEnum(name string, f Field) []string {
	if f["ui:field"] != IconEnumFieldKey {
		return nil
	}
	opts, _ := f["ui:options"].(map[string]any)
	previews, _ := opts["previews"].(map[string]string)
	vals, _ := f["enum"].([]any)
	if len(vals) == 0 {
		return []string{fmt.Sprintf(
			"%s: icon enum without values — the picker would have nothing to show", name)}
	}
	var problems []string
	for _, v := range vals {
		s, ok := v.(string)
		if !ok {
			continue // the null of a nullable field: "no icon" has no preview by construction
		}
		if previews[s] == "" {
			problems = append(problems, fmt.Sprintf(
				"%s: enum value %q has no preview URL — the picker would render an empty tile", name, s))
		}
	}
	return problems
}

// hasType reports whether the field declares the type (["T","null"] included).
func hasType(raw any, want string) bool {
	switch t := raw.(type) {
	case string:
		return t == want
	case []string:
		for _, n := range t {
			if n == want {
				return true
			}
		}
	}
	return false
}

// lintKeyed checks that the two halves of a keyed field agree: the key list
// in ui:options and the property set must match.
//
// Drifted apart, they give exactly the defect this pair must rule out: a key
// is in the switcher but structural validation rejects it
// (additionalProperties:false) - the form does not save and the cause looks
// like a client error. The opposite skew is quieter and therefore worse: the
// schema allows the key, the switcher lacks it, and the value is unreachable.
func lintKeyed(name string, f Field) []string {
	if f["ui:field"] != KeyedFieldKey {
		return nil
	}
	opts, _ := f["ui:options"].(map[string]any)
	keys, _ := opts["keys"].([]Key)
	if len(keys) == 0 {
		return []string{fmt.Sprintf(
			"%s: keyed field without keys — the switcher would have nothing to switch", name)}
	}

	props, _ := f["properties"].(map[string]any)
	var problems []string
	declared := make(map[string]bool, len(keys))
	for _, k := range keys {
		declared[k.Value] = true
		if _, ok := props[k.Value]; !ok {
			problems = append(problems, fmt.Sprintf(
				"%s: key %q is offered by the switcher but missing from properties — "+
					"submitting it would fail structural validation", name, k.Value))
		}
	}
	for _, key := range sortedKeys(props) {
		if !declared[key] {
			problems = append(problems, fmt.Sprintf(
				"%s: property %q is not among the keys — the form can never reach it", name, key))
		}
	}
	if def, ok := opts["default"].(string); ok && !declared[def] {
		problems = append(problems, fmt.Sprintf(
			"%s: default key %q is not in the list — the field would open on a key it cannot edit", name, def))
	}
	// The layout comes from a closed list: a typo ("popver") would otherwise
	// fall back to the client's default silently, visible only by eye.
	if layout, ok := opts["layout"].(string); ok && !keyedLayouts[layout] {
		problems = append(problems, fmt.Sprintf(
			"%s: unknown keyed layout %q — want one of popover/chips/expanded", name, layout))
	}

	// There is no "label without a key" check and cannot be: the label lives
	// INSIDE the key (ui.Key), so it has nowhere to be orphaned.
	return problems
}

// allowedKeywords expands the field's "type" (a string or ["T","null"]) into
// the set of applicable keywords. typed==false means no type is declared.
func allowedKeywords(raw any) (allowed map[string]bool, typed bool) {
	var names []string
	switch t := raw.(type) {
	case string:
		names = []string{t}
	case []string:
		names = t
	default:
		return nil, false
	}
	allowed = map[string]bool{}
	for _, n := range names {
		if n == "null" {
			continue // the null branch neither allows nor forbids anything
		}
		for k := range keywordsByType[n] {
			allowed[k] = true
		}
	}
	return allowed, true
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
