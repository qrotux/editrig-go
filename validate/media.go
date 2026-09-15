package validate

import "encoding/json"

// mediaFieldKey is the "ui:field" value of a media field. A literal rather
// than an import from ui: the core's dependency contract (doc.go) excludes it,
// and mirroring one value is cheaper than pulling in the dependency. The
// second half of the contract is ui.MediaFieldKey, the third the client's
// field-kind key; the agreement of all three is pinned by the application's
// entity test.
const mediaFieldKey = "media"

// MediaKind says how a field accepts file parts.
type MediaKind int

const (
	MediaNone MediaKind = iota
	MediaSingle
	MediaMulti
)

// MediaFieldKind reports whether a field accepts files and how many. The
// read-only check is load-bearing: files are injected AFTER StripReadonly,
// and without it the invariant "Entity.Save physically never sees a read-only
// key" (the write path in the root package) would stop being true.
func MediaFieldKind(uiSchema json.RawMessage, field string) MediaKind {
	if len(uiSchema) == 0 {
		return MediaNone
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(uiSchema, &doc); err != nil {
		return MediaNone
	}
	raw, ok := doc[field]
	if !ok {
		return MediaNone
	}
	var entry map[string]any
	if err := json.Unmarshal(raw, &entry); err != nil {
		return MediaNone
	}
	if entry["ui:readonly"] == true {
		return MediaNone
	}
	if kind, _ := entry["ui:field"].(string); kind != mediaFieldKey {
		return MediaNone
	}
	opts, _ := entry["ui:options"].(map[string]any)
	if multi, _ := opts["multi"].(bool); multi {
		return MediaMulti
	}
	return MediaSingle
}
