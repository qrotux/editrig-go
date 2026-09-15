package ui

// RelationFieldKey is the "ui:field" value of a relation field. A constant
// because the name mirrors the client's field registry: each side of the
// contract holds it in one place, not as a literal in three.
const RelationFieldKey = "relation"

// Relation is a relation field: the value is an id (or an ordered list of
// ids) of another collection, and the form shows LABELS.
//
// collection is the collection name for the widget (what stands behind it is
// known only to the entity: the presentation vocabulary knows no tables). The
// widget asks /options/{field} for values - by FIELD name, not collection;
// collection serves it as a cache key and for debugging.
//
// multi=false is one id as a string; multi=true is an ARRAY of ids whose
// order is significant: it is the order of relations in storage (erjet.Rels
// writes the position into the order column). uniqueItems because a
// duplicate in the form is indistinguishable from one chip, yet in the
// relation table it would double the row.
//
// The renderer is ui:field, NOT ui:widget, for BOTH cardinalities: the multi
// schema is an array, and rjsf routes a widget only for leaf schemas (the
// same reason as JSON and Keyed). One renderer for both cases because they
// differ in cardinality, not behaviour.
//
// Labels of SELECTED values do not live in the declaration: they are
// localized and depend on row data, so they are added per request -
// RelationLabels from Entity.Schema. The search list comes from a separate
// widget request.
func Relation(collection string, multi bool) Field {
	options := map[string]any{"collection": collection, "multi": multi}
	if multi {
		return Field{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"uniqueItems": true,
			"ui:field":    RelationFieldKey,
			"ui:options":  options,
		}
	}
	return Field{
		"type":       "string",
		"ui:field":   RelationFieldKey,
		"ui:options": options,
	}
}

// ParentField makes the options request take its parent from the VALUE of
// the named form field rather than the record id.
//
// Needed by relations whose source is scoped by something other than the row
// owner: on a CREATE form the record has no id yet, but the parent's id is
// already in the form - without this the option list would be empty until
// the first Save.
//
// Merged into ui:options for the same reason as RelationLabels: the .UI
// hatch would replace them wholesale and wipe collection/multi.
func (f Field) ParentField(field string) Field {
	opts, _ := f["ui:options"].(map[string]any)
	next := make(map[string]any, len(opts)+1)
	for k, v := range opts {
		next[k] = v
	}
	next["parentField"] = field
	return f.with("ui:options", next)
}

// ParentFieldName returns the field name declared by ParentField and whether
// one is set. Needed by the declaration gate: a typo in the name gives a
// silently empty select, and only comparing the name against the declaration
// catches it.
func (f Field) ParentFieldName() (string, bool) {
	opts, _ := f["ui:options"].(map[string]any)
	name, ok := opts["parentField"].(string)
	return name, ok
}

// RelationLabels sets labels on the SELECTED values ("uuid" -> "France").
//
// Merged into ui:options rather than through the .UI hatch: that replaces
// ui:options wholesale and would wipe collection/multi, after which the
// widget silently stops searching (the same gotcha as KeyLabels on a keyed
// field).
//
// Values missing from the map are shown by the widget as their own id -
// more honest than an empty chip and needs no second request.
func (f Field) RelationLabels(labels map[string]string) Field {
	opts, _ := f["ui:options"].(map[string]any)
	next := make(map[string]any, len(opts)+1)
	for k, v := range opts {
		next[k] = v
	}
	next["labels"] = labels
	return f.with("ui:options", next)
}

// IsRelation reports "values come from an options source". Media counts too:
// label hydration and the /options/{field} route are shared, only the
// renderer differs.
func (f Field) IsRelation() bool {
	return f["ui:field"] == RelationFieldKey || f["ui:field"] == MediaFieldKey
}
