package validate

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// FieldError is one structural or business validation error; Field is a JSON
// Pointer ("/a", "/nested/n"), and an empty Field means a form-level error.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func compile(schema json.RawMessage) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("editor:///schema.json", doc); err != nil {
		return nil, err
	}
	return c.Compile("editor:///schema.json")
}

// Structural validates `data` (a decoded map) against `schema`; returns
// FieldError per leaf violation, Field = JSON Pointer from InstanceLocation.
func Structural(schema json.RawMessage, data map[string]any) ([]FieldError, error) {
	sch, err := compile(schema)
	if err != nil {
		return nil, err
	}
	if err := sch.Validate(any(data)); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return collectLeaves(ve), nil
		}
		return nil, err
	}
	return nil, nil
}

func collectLeaves(ve *jsonschema.ValidationError) []FieldError {
	p := message.NewPrinter(language.English)
	var out []FieldError
	var walk func(n *jsonschema.ValidationError)
	walk = func(n *jsonschema.ValidationError) {
		if len(n.Causes) == 0 {
			base := "/" + strings.Join(n.InstanceLocation, "/")
			// v6 quirk: a `required` failure sets InstanceLocation to the
			// CONTAINING object; the missing property names live in
			// kind.Required.Missing. Emit one FieldError per missing key so the
			// pointer targets the field (e.g. /username), not the object.
			if req, ok := n.ErrorKind.(*kind.Required); ok {
				for _, k := range req.Missing {
					field := base
					if field == "/" {
						field = "" // root object → "/" + key below
					}
					out = append(out, FieldError{Field: field + "/" + k, Message: n.ErrorKind.LocalizedString(p)})
				}
				return
			}
			out = append(out, FieldError{Field: base, Message: n.ErrorKind.LocalizedString(p)})
			return
		}
		for _, c := range n.Causes {
			walk(c)
		}
	}
	walk(ve)
	return out
}
