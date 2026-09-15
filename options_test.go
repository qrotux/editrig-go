package editrig

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// optionsEntity is fakeEntity plus an options hook recording the last query:
// query-string parsing is checked by WHAT reached the entity, not by handler
// internals.
func optionsEntity(seen *OptionsQuery, seenField *string, out []Option, err error) Entity {
	return optionsEntityWithCatalog(seen, seenField, nil, out, err)
}

// seenCat receives the catalog that reached the hook (nil means not needed).
func optionsEntityWithCatalog(seen *OptionsQuery, seenField *string, seenCat *Catalog, out []Option, err error) Entity {
	e := fakeEntity(newFakeStore())
	// The source is declared for ONE field: "no such field" is the absent key,
	// with no sentinel and no dispatcher branch in the entity.
	e.Options = map[string]OptionSource{
		"tags": func(ctx context.Context, cat Catalog, q OptionsQuery) ([]Option, error) {
			*seen = q
			*seenField = "tags"
			if seenCat != nil {
				*seenCat = cat
			}
			return out, err
		},
	}
	return e
}

// The catalog reaches the options hook as it reaches Schema and Validate: it
// is the ONLY request-scope channel, and the core does not know what the
// application put into it (locale, tenant). Pins that it is this entity's
// catalog, by the key convention.
func TestOptionsReceivesEntityCatalog(t *testing.T) {
	var seen OptionsQuery
	var field string
	var cat Catalog
	hs, _ := newTestHandler(t, optionsEntityWithCatalog(&seen, &field, &cat, nil, nil))
	r := newTestRouter(hs)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/entities/widgets/options/tags", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	if cat == nil {
		t.Fatal("options hook got no catalog")
	}
	if got, want := cat.Title("city"), Key("widgets", "city"); got != want {
		t.Errorf("catalog belongs to another entity: Title = %q, want %q", got, want)
	}
}

func decodeOptions(t *testing.T, body []byte) optionsBody {
	t.Helper()
	var ob optionsBody
	if err := json.Unmarshal(body, &ob); err != nil {
		t.Fatalf("decode options: %v (body=%s)", err, body)
	}
	return ob
}

func TestOptionsHandler(t *testing.T) {
	var seen OptionsQuery
	var field string
	hs, _ := newTestHandler(t, optionsEntity(&seen, &field,
		[]Option{{Value: "1", Label: "One"}, {Value: "2", Label: "Two"}}, nil))
	r := newTestRouter(hs)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/entities/widgets/options/tags?q=on&limit=7", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if field != "tags" {
		t.Errorf("field = %q, want tags", field)
	}
	if seen.Search != "on" {
		t.Errorf("Search = %q, want on", seen.Search)
	}
	if seen.Limit != 7 {
		t.Errorf("Limit = %d, want 7", seen.Limit)
	}
	if len(seen.IDs) != 0 {
		t.Errorf("IDs = %v, want empty", seen.IDs)
	}
	_ = field
	got := decodeOptions(t, rec.Body.Bytes())
	if len(got.Options) != 2 || got.Options[0].Value != "1" || got.Options[0].Label != "One" {
		t.Errorf("options = %+v", got.Options)
	}
}

// An empty result must be [], not null: the client iterates the response
// unguarded, and null would crash the widget instead of showing an empty list.
func TestOptionsEmptyIsArray(t *testing.T) {
	var seen OptionsQuery
	var field string
	hs, _ := newTestHandler(t, optionsEntity(&seen, &field, nil, nil))
	r := newTestRouter(hs)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/entities/widgets/options/tags", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != `{"options":[]}`+"\n" {
		t.Errorf("body = %q, want an empty ARRAY", body)
	}
}

// ids= is the label hydration mode: a list of ids instead of a search. Empty
// elements ("a,,b", a trailing comma) are dropped, or they would reach the IN
// list as an empty string and silently match nothing.
func TestOptionsIDsParsing(t *testing.T) {
	var seen OptionsQuery
	var field string
	hs, _ := newTestHandler(t, optionsEntity(&seen, &field, nil, nil))
	r := newTestRouter(hs)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/entities/widgets/options/tags?ids=a,,b,", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(seen.IDs) != 2 || seen.IDs[0] != "a" || seen.IDs[1] != "b" {
		t.Errorf("IDs = %v, want [a b]", seen.IDs)
	}
}

// Limit: default, ceiling and garbage. The ceiling guards against an outside
// "?limit=1000000": the option collection may be large, and the hook must get
// an already clamped number rather than re-check it.
func TestOptionsLimitBounds(t *testing.T) {
	cases := []struct {
		query string
		want  int
	}{
		{"", optionsDefaultLimit},
		{"?limit=0", optionsDefaultLimit},
		{"?limit=-3", optionsDefaultLimit},
		{"?limit=abc", optionsDefaultLimit},
		{"?limit=999999", optionsMaxLimit},
		{"?limit=25", 25},
	}
	for _, c := range cases {
		var seen OptionsQuery
		var field string
		hs, _ := newTestHandler(t, optionsEntity(&seen, &field, nil, nil))
		r := newTestRouter(hs)

		req := httptest.NewRequest(http.MethodGet, "/api/admin/entities/widgets/options/tags"+c.query, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%q: status = %d, want 200", c.query, rec.Code)
		}
		if seen.Limit != c.want {
			t.Errorf("%q: Limit = %d, want %d", c.query, seen.Limit, c.want)
		}
	}
}

// ?parent= is the id of the row being edited, for sources whose list depends
// on the owner (one user's media library). Trimmed like q/limit/ids: outside
// whitespace must not reach the source as a meaningful id.
func TestOptionsParentParsing(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{"", ""},
		{"?parent=user-1", "user-1"},
		{"?parent=%20user-1%20", "user-1"},
		{"?parent=%20%20", ""},
	}
	for _, c := range cases {
		var seen OptionsQuery
		var field string
		hs, _ := newTestHandler(t, optionsEntity(&seen, &field, nil, nil))
		r := newTestRouter(hs)

		req := httptest.NewRequest(http.MethodGet, "/api/admin/entities/widgets/options/tags"+c.query, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%q: status = %d, want 200", c.query, rec.Code)
		}
		if seen.Parent != c.want {
			t.Errorf("%q: Parent = %q, want %q", c.query, seen.Parent, c.want)
		}
	}
}

// An entity without sources is 404, not 500: "this entity has no relations" is
// a missing resource, not a server failure.
func TestOptionsWithoutHook(t *testing.T) {
	hs, _ := newTestHandler(t, fakeEntity(newFakeStore()))
	r := newTestRouter(hs)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/entities/widgets/options/tags", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}

// An unknown FIELD is an absent key in the source map, and the engine itself
// answers 404: the entity reports nothing, so nothing can drift.
func TestOptionsUnknownField(t *testing.T) {
	var seen OptionsQuery
	var field string
	hs, _ := newTestHandler(t, optionsEntity(&seen, &field, nil, nil))
	r := newTestRouter(hs)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/entities/widgets/options/nope", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestOptionsUnknownEntity(t *testing.T) {
	hs, _ := newTestHandler(t, fakeEntity(newFakeStore()))
	r := newTestRouter(hs)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/entities/nope/options/tags", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestOptionsInfraError(t *testing.T) {
	var seen OptionsQuery
	var field string
	hs, _ := newTestHandler(t, optionsEntity(&seen, &field, nil, errors.New("boom")))
	r := newTestRouter(hs)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/entities/widgets/options/tags", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body=%s)", rec.Code, rec.Body.String())
	}
}

// Option sources are OPTIONAL: the registry must accept an entity without them
// (its own contract stays the five hooks).
func TestRegistryAcceptsEntityWithoutOptions(t *testing.T) {
	if _, err := NewRegistry(fakeEntity(newFakeStore())); err != nil {
		t.Fatalf("NewRegistry without Options: %v", err)
	}
}
