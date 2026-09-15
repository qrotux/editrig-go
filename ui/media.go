package ui

// MediaFieldKey is the "ui:field" value of a media field. It mirrors the
// client's field registry key and the engine's unexported mediaFieldKey (the
// engine does not import this package - see doc.go).
const MediaFieldKey = "media"

// MediaAspects is the closed list of preview aspect ratios. Closed because
// the value becomes a CSS class: a typo would otherwise fall back to the
// client's default silently, visible only by eye on a live form.
var MediaAspects = map[string]bool{"1:1": true, "2:1": true, "4:3": true, "3:2": true}

// Media is an image field: a media row id on the wire, a THUMBNAIL in the
// form.
//
// It is a RELATION field with another renderer, not a separate protocol
// concept: the value source, /options/{field} and hydration of the selected
// value are shared, and the option label is the thumbnail URL. A separate
// Option.Preview would cost three new core concepts for the same thing.
func Media(collection, aspect string) Field {
	f := Relation(collection, false)
	f["ui:field"] = MediaFieldKey
	opts, _ := f["ui:options"].(map[string]any)
	opts["aspect"] = aspect
	return f
}

// MediaMulti is an ordered SET of images: an array of media row ids on the
// wire, a grid of thumbnails in the form.
//
// The counterpart of Media in everything but cardinality, built the same way
// on top of Relation: renderer and value source are shared, only the count
// differs. That is exactly why it is a separate constructor and not
// List(Media(...)): in a wrapper ui:field goes to uiSchema.items, and both
// mechanisms that read the marker on the field itself (multipart file
// injection in the core's write path and label hydration via IsRelation) stop seeing the
// field as media.
//
// The storage counterpart is erjet.DeferredList over erjet.ChildValues.
func MediaMulti(collection, aspect string) Field {
	f := Relation(collection, true)
	f["ui:field"] = MediaFieldKey
	opts, _ := f["ui:options"].(map[string]any)
	opts["aspect"] = aspect
	return f
}

// Generate declares "this field can generate an image": under the `generate`
// key of ui:options travels a READY map built by the application (endpoint,
// count ceiling and a parameter list with already localized labels).
//
// The vocabulary knows NOTHING about its contents - just as MaxBytes below
// knows nothing about the application's size limits. Generation drags in an
// image provider, prompts and an LLM, none of which belong here. Only two
// parties can parse this map, both outside the core: the application that
// built it and the client component that draws selects from it.
//
// An absent key is a valid state, not a forgotten call: the application does
// not declare generate when no provider is configured, and then the form has
// no button at all.
func (f Field) Generate(spec map[string]any) Field {
	opts, _ := f["ui:options"].(map[string]any)
	next := make(map[string]any, len(opts)+1)
	for k, v := range opts {
		next[k] = v
	}
	next["generate"] = spec
	return f.with("ui:options", next)
}

// MaxBytes is the file size ceiling, the same one the server applies in
// Validate. Optional and passed as a separate call rather than a third
// Media() argument: the application knows its media categories and their
// limits, ui does not and must not, so the number comes from the caller,
// already read from the single source of truth.
//
// Without it the client's fast rejection would be a hard-coded constant
// shared by all categories: an avatar limited to 5 MiB on the server would
// pass a flat 10 MiB client check and fail only after a full multipart round
// trip - exactly what the client check exists to avoid.
func (f Field) MaxBytes(n int64) Field {
	opts, _ := f["ui:options"].(map[string]any)
	next := make(map[string]any, len(opts)+1)
	for k, v := range opts {
		next[k] = v
	}
	next["maxBytes"] = n
	return f.with("ui:options", next)
}
