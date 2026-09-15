package validate

import (
	"encoding/json"
	"sort"
	"strings"
)

func pointerToken(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// UnknownFieldPolicy controls submitted top-level keys absent from schema.properties.
type UnknownFieldPolicy string

const (
	AllowUnknownFields  UnknownFieldPolicy = "allow"
	RejectUnknownFields UnknownFieldPolicy = "reject"
	StripUnknownFields  UnknownFieldPolicy = "strip"
)

// UnknownFields applies policy to the top-level keys of in that are not in
// the schema's properties: reject reports one FieldError per key, strip
// deletes them in place, allow does nothing.
func UnknownFields(schema json.RawMessage, in map[string]any, policy UnknownFieldPolicy) ([]FieldError, error) {
	if policy == AllowUnknownFields || policy == "" {
		return nil, nil
	}
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &doc); err != nil {
		return nil, err
	}
	var keys []string
	for key := range in {
		if _, ok := doc.Properties[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var out []FieldError
	for _, key := range keys {
		if policy == StripUnknownFields {
			delete(in, key)
		} else {
			out = append(out, FieldError{Field: "/" + pointerToken(key), Message: "unknown field"})
		}
	}
	return out, nil
}
