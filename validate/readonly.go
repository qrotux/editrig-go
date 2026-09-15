package validate

import (
	"encoding/json"
	"sort"
	"strings"
)

// ReadonlyFields collects the names of the fields the entity declared
// READ-ONLY, that is whose uiSchema entry carries "ui:readonly": true.
//
// The flag is declared ONCE, on the field, and the engine DERIVES it: there is
// no second, root-level list of read-only fields, so nothing can drift.
//
// Top-level keys with the "ui:" prefix are root extensions (ui:order,
// ui:groups, ui:header), not fields: a property name cannot start with "ui:",
// so the test is unambiguous.
//
// A broken or empty uiSchema gives nil (fail-open for rendering; the real
// write boundary is StripReadonly below).
func ReadonlyFields(uiSchema json.RawMessage) []string {
	if len(uiSchema) == 0 {
		return nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(uiSchema, &doc); err != nil {
		return nil
	}
	var fields []string
	for key, raw := range doc {
		if strings.HasPrefix(key, uiKeyPrefix) {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(raw, &entry); err != nil {
			continue // not an object, not a field entry
		}
		if readonly, _ := entry["ui:readonly"].(bool); readonly {
			fields = append(fields, key)
		}
	}
	// Map order is random and the result ends up in dropped[] and from there in
	// the core's log; sorted so the log is stable.
	sort.Strings(fields)
	return fields
}

const uiKeyPrefix = "ui:"

// StripReadonly removes from the incoming payload every top-level key declared
// read-only and returns the names actually removed.
//
// The engine calls it (create/update) BEFORE structural validation, Validate
// and Save, so Entity.Save physically never sees a read-only key: a stale or
// forged form cannot overwrite values the admin does not own (ratings,
// counters, worker-maintained timestamps). Mutates in in place: it is the
// freshly decoded request body and has no other owner.
func StripReadonly(uiSchema json.RawMessage, in map[string]any) []string {
	if in == nil {
		return nil
	}
	var dropped []string
	for _, f := range ReadonlyFields(uiSchema) {
		if _, ok := in[f]; ok {
			delete(in, f)
			dropped = append(dropped, f)
		}
	}
	return dropped
}
