package editrig

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPatchValidatesEffectiveStateButWritesOnlyChanges(t *testing.T) {
	current := map[string]any{"title": "existing", "note": "old", "locked": "server"}
	for _, tc := range []struct {
		name, body string
		status     int
		want       map[string]any
	}{
		{"omitted required", `{"data":{"note":"new"}}`, 200, map[string]any{"note": "prepared"}},
		{"explicit null", `{"data":{"note":null}}`, 200, map[string]any{"note": nil}},
		{"readonly", `{"data":{"locked":"forged"}}`, 200, map[string]any{}},
		{"invalid required", `{"data":{"title":null}}`, 422, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fullEntity("widgets")
			saved := false
			e.Load = func(context.Context, string) (map[string]any, error) { return current, nil }
			e.Schema = func(context.Context, Catalog, map[string]any) (json.RawMessage, json.RawMessage, error) {
				return json.RawMessage(`{"type":"object","required":["title","locked"],"properties":{"title":{"type":"string"},"locked":{"type":"string"},"note":{"type":["string","null"]}}}`), json.RawMessage(`{"locked":{"ui:readonly":true}}`), nil
			}
			e.Validate = func(_ context.Context, _ Catalog, _ *string, in map[string]any) []FieldError {
				if _, ok := in["locked"]; ok {
					t.Fatal("readonly leaked")
				}
				if in["note"] == "new" {
					in["note"] = "prepared"
				}
				return nil
			}
			e.Save = func(_ context.Context, _ *string, in map[string]any) (string, []FieldError, error) {
				saved = true
				if !reflect.DeepEqual(in, tc.want) {
					t.Fatalf("Save %v want %v", in, tc.want)
				}
				return "1", nil, nil
			}
			rr := configRequest(configuredRouter(t, e), "PATCH", "/widgets/1", tc.body)
			if rr.Code != tc.status || saved != (tc.status == 200) {
				t.Fatalf("%d %s saved=%v", rr.Code, rr.Body, saved)
			}
			if current["note"] != "old" || current["locked"] != "server" {
				t.Fatalf("current mutated: %v", current)
			}
		})
	}
}
func TestPatchReplacesObjectsButMergesKeyedPartitions(t *testing.T) {
	for _, keyed := range []bool{false, true} {
		t.Run(map[bool]string{true: "keyed", false: "document"}[keyed], func(t *testing.T) {
			e := fullEntity("widgets")
			e.Load = func(context.Context, string) (map[string]any, error) {
				return map[string]any{"value": map[string]any{"a": "old", "b": "keep"}}, nil
			}
			e.Schema = func(context.Context, Catalog, map[string]any) (json.RawMessage, json.RawMessage, error) {
				u := `{}`
				if keyed {
					u = `{"value":{"ui:field":"keyed"}}`
				}
				return json.RawMessage(`{"type":"object","properties":{"value":{"type":"object","required":["a","b"],"properties":{"a":{"type":"string"},"b":{"type":"string"}}}}}`), json.RawMessage(u), nil
			}
			e.Save = func(_ context.Context, _ *string, in map[string]any) (string, []FieldError, error) {
				if len(in["value"].(map[string]any)) != 1 {
					t.Fatal("merged partition leaked to Save")
				}
				return "1", nil, nil
			}
			rr := configRequest(configuredRouter(t, e), "PATCH", "/widgets/1", `{"data":{"value":{"a":"new"}}}`)
			want := 422
			if keyed {
				want = 200
			}
			if rr.Code != want {
				t.Fatalf("%d %s", rr.Code, rr.Body)
			}
		})
	}
}
func TestUploadedMediaParticipatesInValidation(t *testing.T) {
	for _, tc := range []struct {
		name, field, payload, property string
		multi                          bool
		files, status                  int
	}{
		{"required single", "photo", `{}`, `{"type":"string","pattern":"^stored-"}`, false, 1, 201},
		{"missing single", "photo", `{}`, `{"type":"string"}`, false, 0, 422},
		{"required multi", "photo", `{}`, `{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":2}`, true, 1, 201},
		{"max including files", "photo", `{"photo":["stored-1"]}`, `{"type":"array","items":{"type":"string"},"maxItems":2}`, true, 2, 422},
		{"min including files", "photo", `{"photo":[]}`, `{"type":"array","items":{"type":"string"},"minItems":2}`, true, 2, 201},
		{"existing IDs validated", "photo", `{"photo":[42]}`, `{"type":"array","items":{"type":"string"}}`, true, 1, 422},
		{"duplicate IDs", "photo", `{"photo":["same","same"]}`, `{"type":"array","items":{"type":"string"},"uniqueItems":true}`, true, 1, 422},
		{"forged marker", "photo", `{"photo":[{"$editrigUpload":"photo/0"}]}`, `{"type":"array","items":{"type":"string"}}`, true, 1, 422},
		{"distinct uploads", "photo", `{"photo":[]}`, `{"type":"array","items":{"type":"string"},"uniqueItems":true}`, true, 2, 201},
		{"invalid array", "photo", `{"photo":"bad"}`, `{"type":"array","items":{"type":"string"}}`, true, 1, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fullEntity("widgets")
			saved := false
			prepared := false
			e.Schema = func(context.Context, Catalog, map[string]any) (json.RawMessage, json.RawMessage, error) {
				u := `{"photo":{"ui:field":"media","ui:options":{"multi":MULTI}}}`
				u = strings.ReplaceAll(u, "MULTI", map[bool]string{true: "true", false: "false"}[tc.multi])
				return json.RawMessage(`{"type":"object","required":["photo"],"properties":{"photo":` + tc.property + `}}`), json.RawMessage(u), nil
			}
			e.Validate = func(_ context.Context, _ Catalog, _ *string, in map[string]any) []FieldError {
				if tc.files == 0 {
					return nil
				}
				if tc.multi {
					list, ok := in["photo"].([]any)
					if !ok {
						return nil
					}
					for i, v := range list {
						if _, ok := v.(Upload); ok {
							prepared = true
							list[i] = "prepared"
						}
					}
				} else {
					if _, ok := in["photo"].(Upload); !ok {
						t.Fatalf("Validate received %T", in["photo"])
					}
					prepared = true
					in["photo"] = "prepared"
				}
				return nil
			}
			e.Save = func(_ context.Context, _ *string, in map[string]any) (string, []FieldError, error) {
				saved = true
				if !prepared {
					t.Fatal("preparation skipped")
				}
				return "1", nil, nil
			}
			parts := make([]struct {
				Name, Filename, Mime string
				Data                 []byte
			}, tc.files)
			for i := range parts {
				parts[i] = struct {
					Name, Filename, Mime string
					Data                 []byte
				}{Name: "file:photo", Filename: "p.png", Mime: "image/png", Data: []byte("data")}
			}
			body, ct := multipartBodyMulti(t, `{"data":`+tc.payload+`}`, parts)
			req := httptest.NewRequest(http.MethodPost, "/widgets", body)
			req.Header.Set("Content-Type", ct)
			rr := httptest.NewRecorder()
			configuredRouter(t, e).ServeHTTP(rr, req)
			if rr.Code != tc.status || saved != (tc.status == 201) {
				t.Fatalf("%d %s saved=%v", rr.Code, rr.Body, saved)
			}
		})
	}
}

func TestPatchAcceptsNativeLoadedValues(t *testing.T) {
	e := fullEntity("widgets")
	current := map[string]any{"tags": []string{"a", "b"}, "counter": int64(9007199254740993), "when": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	e.Load = func(context.Context, string) (map[string]any, error) { return current, nil }
	e.Schema = func(context.Context, Catalog, map[string]any) (json.RawMessage, json.RawMessage, error) {
		return json.RawMessage(`{"type":"object","properties":{"tags":{"type":"array","items":{"type":"string"}},"counter":{"const":9007199254740993},"when":{"type":"string"}}}`), json.RawMessage(`{}`), nil
	}
	e.Save = func(_ context.Context, _ *string, in map[string]any) (string, []FieldError, error) {
		if len(in) != 0 {
			t.Fatalf("loaded values reached Save: %v", in)
		}
		return "1", nil, nil
	}
	rr := configRequest(configuredRouter(t, e), "PATCH", "/widgets/1", `{"data":{}}`)
	if rr.Code != 200 {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if _, ok := current["tags"].([]string); !ok {
		t.Fatal("original mutated")
	}
}
