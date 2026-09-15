// Command memory runs an editor backed by an in-memory store.
package main

import (
	"context"
	"encoding/json"
	"log"
	"maps"
	"net/http"
	"strconv"
	"sync"

	editrig "github.com/qrotux/editrig-go"
	"github.com/qrotux/editrig-go/router/erstd"
	"github.com/qrotux/editrig-go/ui"
)

func main() {
	log.Print("Editor API: http://127.0.0.1:8080/articles/schema")
	log.Fatal(http.ListenAndServe("127.0.0.1:8080", newHandler()))
}

func newHandler() http.Handler {
	var mu sync.Mutex
	rows := map[string]map[string]any{}
	next := 0
	entity := editrig.Entity{
		Name: "articles",
		Schema: func(_ context.Context, cat editrig.Catalog, _ map[string]any) (json.RawMessage, json.RawMessage, error) {
			return (ui.Document{Fields: map[string]ui.Field{
				"id":    ui.String().Readonly().Hidden(),
				"title": ui.String().Title(cat.Title("title")).Required().MinLen(1),
				"note":  ui.String().Title(cat.Title("note")).Nullable(),
			}, Order: []string{"title", "note", "id"}}).Marshal()
		},
		Load: func(_ context.Context, id string) (map[string]any, error) {
			mu.Lock()
			defer mu.Unlock()
			return maps.Clone(rows[id]), nil
		},
		Save: func(_ context.Context, id *string, in map[string]any) (string, []editrig.FieldError, error) {
			mu.Lock()
			defer mu.Unlock()
			var key string
			if id == nil {
				next++
				key = strconv.Itoa(next)
				rows[key] = map[string]any{"id": key}
			} else {
				key = *id
				if rows[key] == nil {
					return "", nil, editrig.ErrNotFound
				}
			}
			for field, value := range in {
				if field == "title" || field == "note" {
					rows[key][field] = value
				}
			}
			return key, nil, nil
		},
		Delete: func(_ context.Context, id string) error { mu.Lock(); defer mu.Unlock(); delete(rows, id); return nil },
	}
	reg, err := editrig.NewRegistry(entity)
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	erstd.Register(mux, "", nil, editrig.NewHandler(reg, nil, nil, editrig.WithBodyLimits(editrig.BodyLimits{JSONBytes: 1 << 20})))
	return mux
}
