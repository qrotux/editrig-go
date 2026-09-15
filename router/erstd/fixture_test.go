package erstd

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"testing"

	editrig "github.com/qrotux/editrig-go"
)

// memEntity is one entity over a map: enough to prove every route reaches
// the right method with the right params.
func memEntity(t *testing.T) *editrig.Handler {
	t.Helper()
	var mu sync.Mutex
	rows := map[string]map[string]any{}
	next := 0
	e := editrig.Entity{
		Name: "articles",
		Schema: func(context.Context, editrig.Catalog, map[string]any) (json.RawMessage, json.RawMessage, error) {
			return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"title":{"type":"string"},"tag":{"type":"string"}},"required":["title"]}`),
				json.RawMessage(`{"id":{"ui:readonly":true},"tag":{"ui:field":"relation"}}`), nil
		},
		Load: func(_ context.Context, id string) (map[string]any, error) {
			mu.Lock()
			defer mu.Unlock()
			row, ok := rows[id]
			if !ok {
				return nil, nil
			}
			out := map[string]any{}
			for k, v := range row {
				out[k] = v
			}
			return out, nil
		},
		Save: func(_ context.Context, id *string, in map[string]any) (string, []editrig.FieldError, error) {
			mu.Lock()
			defer mu.Unlock()
			key := ""
			if id == nil {
				next++
				key = strconv.Itoa(next)
				rows[key] = map[string]any{"id": key}
			} else {
				key = *id
				if _, ok := rows[key]; !ok {
					return "", nil, editrig.ErrNotFound
				}
			}
			for k, v := range in {
				rows[key][k] = v
			}
			return key, nil, nil
		},
		Delete: func(_ context.Context, id string) error {
			mu.Lock()
			defer mu.Unlock()
			delete(rows, id)
			return nil
		},
		Options: map[string]editrig.OptionSource{
			"tag": func(context.Context, editrig.Catalog, editrig.OptionsQuery) ([]editrig.Option, error) {
				return []editrig.Option{{Value: "t1", Label: "Tag 1"}}, nil
			},
		},
	}
	reg, err := editrig.NewRegistry(e)
	if err != nil {
		t.Fatal(err)
	}
	return editrig.NewHandler(reg, nil, nil)
}

// passGuard proves Register wraps every route: it only stamps a header.
func passGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Guard", "1")
		next.ServeHTTP(w, r)
	})
}
