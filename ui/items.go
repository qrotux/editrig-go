package ui

// Items is an array of OBJECTS: a repeatable block whose element has fields
// of its own.
//
// The counterpart of List in structure (an ordered list, order significant)
// but on another axis: a List element is a nameless scalar, an Items element
// is an object with its own declaration. Hence the one behavioural
// difference: .Required() INSIDE the element is expressible and works - an
// object has its own "required" array, unlike the nameless list element (see
// the panic in List).
//
// No custom renderer: rjsf draws an array of objects itself, and the theme
// ships ArrayFieldTemplate and ArrayFieldItemTemplate with add/remove/move.
// The element's ui half must go to uiSchema.items - the only place rjsf takes
// it from (computeItemUiSchema); that is what the $itemUI marker is for, the
// same one List uses.
//
// The CONSTRUCTOR TAKES NO ARGUMENTS; document assembly fills in the element
// (decl.Set.Fields -> WithItems). The reason is not stylistic: the element
// arrives as a nested decl.Set, and ui CANNOT import decl - decl imports ui,
// and the reverse import would be a cycle. The side benefit outweighs the
// reason: labels of subfields, enums and keyed keys inside the element are
// set by the same Fields recursion as at the top level - no second labelling
// mechanism.
//
// The storage counterpart is erjet.ChildRows.
func Items() Field {
	return Field{"type": "array", itemsKey: true}
}

// SubfieldSuffix is the catalog key suffix of an element subfield:
// editor.<entity>.<field>_fields.<sub>.
//
// It mirrors editrig.FieldKey - the engine and the vocabulary do not import
// each other (see doc.go), so the literal lives once in each, as
// ui.MediaFieldKey mirrors the engine's unexported mediaFieldKey. Change one,
// change the other; a drift gives a silent catalog fallback (the raw key
// shows in the form instead of a label).
const SubfieldSuffix = "_fields."

// itemsKey marks "this is Items, not List". Both look the same on the schema
// half ("type":"array" plus an "items" key), and without an explicit flag
// neither document assembly (which recursion to apply to the element - a
// decl.Set or a bare Field) nor the lint (which message to print) could tell
// them apart. A third namespace inside the map, next to requiredKey and
// itemUIKey, for the same reason: it is not on the wire - Split drops it
// with the other markers.
//
// It does NOT mean "the element is not filled in yet": WithItems clones the
// field (see clone) and carries the marker as is, so IsItems() is true both
// before and after WithItems - unlike itemUIKey/requiredKey, which appear
// only with their content. The lint detects an empty element by a DIFFERENT
// sign - absent (or empty) items.properties, see lintNested in lint.go.
const itemsKey = "$items"

// IsItems reports whether the field is declared as a list of objects.
// Exported for the same reason as IsKeyed: document assembly fills in the
// element, and the lint checks that it is filled.
func (f Field) IsItems() bool {
	b, _ := f[itemsKey].(bool)
	return b
}

// IsKeyedItems reports whether the field is a keyed field whose every key
// holds a list of objects (ui.Keyed(ui.Items(), keys)). Exported for the same
// reason as IsItems: document assembly picks WithKeyedItems by it, the lints
// their own branch.
func (f Field) IsKeyedItems() bool {
	b, _ := f[keyedItemsKey].(bool)
	return b
}

// WithItems fills in the element: the subfields' schema halves go to items,
// their ui halves and order to the $itemUI marker.
//
// Called by document assembly (decl.Set.Fields), not by the declaration:
// labels are localized and come from the catalog, while the declaration is
// static - the same split of roles as EnumLabels and KeyLabels.
func (f Field) WithItems(fields map[string]Field, order []string) Field {
	props, entries := Split(fields)

	item := map[string]any{
		"type":       "object",
		"properties": props,
		// An unknown subfield must become a 422 from structural validation, not
		// reach the store: there it would vanish silently (Assignments finds no
		// cell), and the admin would not learn that part of the data was lost.
		"additionalProperties": false,
	}
	if req := requiredOf(fields, order); len(req) > 0 {
		item["required"] = req
	}

	out := f.clone()
	out["items"] = item

	entry := make(map[string]any, len(entries)+1)
	for name, e := range entries {
		entry[name] = e
	}
	if len(order) > 0 {
		// ui:order INSIDE the element: without it rjsf takes the order of the
		// property map walk, i.e. alphabetical - the same reason ui:order is
		// mandatory at the top level (document.go).
		entry["ui:order"] = order
	}
	if len(entry) > 0 {
		out[itemUIKey] = entry
	}
	return out
}

// WithKeyedItems fills in the element of a keyed field (Keyed(Items(),
// keys)): the object items schema goes into an INDEPENDENT copy of every
// key's property, the subfields' ui halves and order into
// ui:options.inner["items"] - the only place the KeyedField -> ObjectField ->
// ArrayField.computeItemUiSchema chain reads them from.
//
// Copy-on-write for the WHOLE DEPTH of the patch, not just the top level:
// clone copies only the top map, and the clone's properties/ui:options are
// the same objects as the DECLARATION field's (a package-level var that
// outlives the request), while Fields is called per request with its own
// catalog. Mutating inside means concurrent map writes under load and
// localized titles leaking between locales, invisible to goldens. The element
// schema is rebuilt FOR EVERY key (a fresh WithItems): copies independent of
// each other is the same invariant as for the keys in Keyed itself.
//
// Not a rebuild via Keyed(inner.WithItems(...), keys): that would lose the
// modifiers applied after Keyed at the call site (KeyedLayout, ColSpan).
func (f Field) WithKeyedItems(fields map[string]Field, order []string) Field {
	out := f.clone()

	props, _ := f["properties"].(map[string]any)
	nextProps := make(map[string]any, len(props))
	var itemUI any
	for key, raw := range props {
		prop, _ := raw.(map[string]any)
		next := make(map[string]any, len(prop)+1)
		for k, v := range prop {
			next[k] = v
		}
		// Fresh per key: WithItems runs the subfields through Split, and every
		// map in the result is new - the copies share no object.
		composed := Items().WithItems(fields, order)
		next["items"] = composed["items"]
		// The element's ui half is the same for every build and shares no map
		// with the schema half (Split lays them into separate trees) - any one
		// will do, no extra build needed for it.
		itemUI = composed[itemUIKey]
		nextProps[key] = next
	}
	out["properties"] = nextProps

	opts, _ := f["ui:options"].(map[string]any)
	nextOpts := make(map[string]any, len(opts)+1)
	for k, v := range opts {
		nextOpts[k] = v
	}
	// inner exists only if the inner field carried ui keys; bare ui.Items()
	// has none - create it, or copy the existing one without losing keys.
	prevInner, _ := opts["inner"].(map[string]any)
	nextInner := make(map[string]any, len(prevInner)+1)
	for k, v := range prevInner {
		nextInner[k] = v
	}
	if itemUI == nil && len(props) == 0 {
		// Degenerate zero-key case (lintKeyed flags it anyway): the loop did not
		// run, so the ui half comes from a separate build, like the keys' schema.
		itemUI = Items().WithItems(fields, order)[itemUIKey]
	}
	if itemUI != nil {
		nextInner["items"] = itemUI
	}
	nextOpts["inner"] = nextInner
	out["ui:options"] = nextOpts
	return out
}
