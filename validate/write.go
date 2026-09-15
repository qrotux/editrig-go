package validate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strings"
)

// Write validates an update as the row's current state overlaid with the
// submitted top-level keys: keyed partitions merge one level, documents and
// arrays are replacement values. files counts the pending uploads per field;
// they enter the overlay as markers the schema is widened to accept, so
// required/minItems/maxItems see the final list without an Upload ever being
// validated as an id string. On create current is nil.
func Write(schema, uiSchema json.RawMessage, current, in map[string]any, files map[string]int) ([]FieldError, error) {
	var ui map[string]map[string]any
	// Root UI extensions are not field objects, so decode entries individually.
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(uiSchema, &entries); err != nil && len(uiSchema) > 0 {
		return nil, err
	}
	ui = make(map[string]map[string]any, len(entries))
	for name, raw := range entries {
		var field map[string]any
		if json.Unmarshal(raw, &field) == nil {
			ui[name] = field
		}
	}
	// Normalize a detached copy before merging; native typed keyed maps must
	// preserve untouched partitions too. UseNumber keeps bigint values exact.
	var loaded map[string]any
	if current != nil {
		raw, err := json.Marshal(current)
		if err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&loaded); err != nil {
			return nil, err
		}
	}
	current = loaded
	data := make(map[string]any, len(current)+len(in))
	maps.Copy(data, current)
	for name, v := range in {
		if ui[name]["ui:field"] == "keyed" {
			old, oldOK := current[name].(map[string]any)
			next, nextOK := v.(map[string]any)
			if oldOK && nextOK {
				merged := maps.Clone(old)
				maps.Copy(merged, next)
				data[name] = merged
				continue
			}
		}
		data[name] = v
	}
	if len(files) == 0 {
		return Structural(schema, data)
	}
	var root any
	if err := json.Unmarshal(schema, &root); err != nil {
		return nil, err
	}
	doc, ok := root.(map[string]any)
	if !ok {
		return Structural(schema, data)
	}
	props, _ := doc["properties"].(map[string]any)
	for name, count := range files {
		kind := MediaFieldKind(uiSchema, name)
		if count == 0 || kind == MediaNone || (kind == MediaSingle && count != 1) {
			continue
		}
		for _, keyword := range []string{"$ref", "$dynamicRef", "$recursiveRef", "allOf", "anyOf", "oneOf", "not", "if", "then", "else", "dependentSchemas", "dependencies", "patternProperties"} {
			if _, present := doc[keyword]; present {
				return nil, fmt.Errorf("editrig: upload schema requires direct root properties; unsupported %s", keyword)
			}
		}
		if _, present := props[name]; !present {
			return nil, fmt.Errorf("editrig: upload field %q must be declared in root properties", name)
		}
		// Objects distinguish pending uploads from every persisted string ID, including
		// forged strings resembling a marker. Markers never reach Validate or Save.
		markers := make([]any, count)
		for i := range markers {
			marker := map[string]any{"$editrigUpload": fmt.Sprintf("%s/%d", name, i)}
			existing, _ := in[name].([]any)
			for suffix := 0; markerIn(existing, marker); suffix++ {
				marker["$editrigUpload"] = fmt.Sprintf("%s/%d/%d", name, i, suffix)
			}
			markers[i] = marker
		}
		if kind == MediaSingle {
			data[name] = markers[0]
			if prop, present := props[name]; present {
				props[name] = map[string]any{"anyOf": []any{prop, map[string]any{"enum": markers}}}
			}
			continue
		}
		existing := in[name]
		list, valid := existing.([]any)
		if existing != nil && !valid {
			data[name] = existing
			continue
		}
		final := append(append([]any{}, list...), markers...)
		data[name] = final
		if prop, present := props[name]; present {
			expanded, err := uploadArraySchema(prop, root, markers, map[string]bool{})
			if err != nil {
				return nil, err
			}
			props[name] = expanded
		}
	}
	adjusted, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	return Structural(adjusted, data)
}

// uploadArraySchema allows pending uploads at item positions while retaining
// array constraints and validation of stored IDs. Local refs and allOf are supported.
func uploadArraySchema(node, root any, markers []any, seen map[string]bool) (any, error) {
	obj, ok := node.(map[string]any)
	if !ok {
		return node, nil
	}
	out := maps.Clone(obj)
	for _, keyword := range []string{"$id", "$dynamicRef", "$recursiveRef", "anyOf", "oneOf", "not", "if", "then", "else", "contains", "prefixItems", "unevaluatedItems", "enum", "const"} {
		if _, present := out[keyword]; present {
			return nil, fmt.Errorf("editrig: media upload array does not support %s", keyword)
		}
	}
	if ref, ok := out["$ref"].(string); ok {
		if seen[ref] {
			return nil, fmt.Errorf("editrig: recursive media array schema %q", ref)
		}
		if !strings.HasPrefix(ref, "#/") {
			return nil, fmt.Errorf("editrig: media array requires a local schema reference: %q", ref)
		}
		target := root
		for _, token := range strings.Split(ref[2:], "/") {
			key := strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
			m, ok := target.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("editrig: invalid media schema reference %q", ref)
			}
			target, ok = m[key]
			if !ok {
				return nil, fmt.Errorf("editrig: missing media schema reference %q", ref)
			}
		}
		branch := maps.Clone(seen)
		branch[ref] = true
		expanded, err := uploadArraySchema(target, root, markers, branch)
		if err != nil {
			return nil, err
		}
		delete(out, "$ref")
		// Sibling assertions apply alongside the referenced schema.
		rest, err := uploadArraySchema(out, root, markers, seen)
		if err != nil {
			return nil, err
		}
		return map[string]any{"allOf": []any{expanded, rest}}, nil
	}
	if item, ok := out["items"]; ok {
		if _, tuple := item.([]any); tuple {
			return nil, fmt.Errorf("editrig: media arrays require homogeneous items")
		}
		out["items"] = map[string]any{"anyOf": []any{item, map[string]any{"enum": markers}}}
	}
	for _, key := range []string{"allOf"} {
		if branches, ok := out[key].([]any); ok {
			next := make([]any, len(branches))
			for i, branch := range branches {
				var err error
				next[i], err = uploadArraySchema(branch, root, markers, seen)
				if err != nil {
					return nil, err
				}
			}
			out[key] = next
		}
	}
	return out, nil
}

func markerIn(values []any, marker any) bool {
	for _, value := range values {
		if reflect.DeepEqual(value, marker) {
			return true
		}
	}
	return false
}
