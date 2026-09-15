package editrig

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestUpdateDropsReadonlyBeforeSave pins the engine gate: a PATCH with a
// read-only key delivers it neither to Validate nor to Save. This is what
// protects worker-owned values (ratings, counters) from a stale form.
func TestUpdateDropsReadonlyBeforeSave(t *testing.T) {
	store := newFakeStore()
	e := fakeEntity(store)
	base := e.Schema
	e.Schema = func(ctx context.Context, cat Catalog, data map[string]any) (json.RawMessage, json.RawMessage, error) {
		sch, _, err := base(ctx, cat, data)
		if err != nil {
			return nil, nil, err
		}
		// "b" is declared read-only, "a" is editable.
		return sch, json.RawMessage(`{"b":{"ui:readonly":true}}`), nil
	}
	var sawValidate map[string]any
	e.Validate = func(ctx context.Context, cat Catalog, id *string, in map[string]any) []FieldError {
		sawValidate = in
		return nil
	}
	baseSave := e.Save
	var sawSave map[string]any
	e.Save = func(ctx context.Context, id *string, in map[string]any) (string, []FieldError, error) {
		sawSave = in
		return baseSave(ctx, id, in)
	}

	store.data["1"] = map[string]any{"a": "keep", "b": "worker-owned"}

	hs, _ := newTestHandler(t, e)
	r := newTestRouter(hs)
	body := bytes.NewBufferString(`{"data":{"a":"edited","b":"clobbered"}}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/entities/widgets/1", body)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if _, ok := sawValidate["b"]; ok {
		t.Errorf("Validate saw read-only key: %v", sawValidate)
	}
	if _, ok := sawSave["b"]; ok {
		t.Errorf("Save saw read-only key: %v", sawSave)
	}
	if sawSave["a"] != "edited" {
		t.Errorf("editable key lost: %v", sawSave)
	}
}
