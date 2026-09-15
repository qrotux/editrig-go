// Package routertest drives the six editrig routes through a mounted handler
// so every router package pins the same route-to-method contract.
package routertest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Run walks create, schema, load, update, options and delete under base and
// checks status codes, the id flowing between calls, and that the guard
// header (X-Guard, if the mount uses one) is present on every route.
func Run(t *testing.T, h http.Handler, base string) {
	t.Helper()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, base+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	guarded := false
	check := func(rr *httptest.ResponseRecorder, want int, what string) {
		t.Helper()
		if rr.Code != want {
			t.Fatalf("%s: status = %d, want %d (body=%s)", what, rr.Code, want, rr.Body.String())
		}
		if rr.Header().Get("X-Guard") == "1" {
			guarded = true
		} else if guarded {
			t.Errorf("%s: guard not applied", what)
		}
	}

	check(do("GET", "/articles/schema", ""), 200, "schema")
	check(do("GET", "/nope/schema", ""), 400, "unknown entity")

	rr := do("POST", "/articles", `{"data":{"title":"one","tag":"t1"}}`)
	check(rr, 201, "create")
	var env struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil || env.ID == "" {
		t.Fatalf("create envelope: %s", rr.Body.String())
	}

	check(do("GET", "/articles/"+env.ID, ""), 200, "load")
	check(do("GET", "/articles/missing", ""), 404, "load missing")
	check(do("PATCH", "/articles/"+env.ID, `{"data":{"title":"two"}}`), 200, "update")
	check(do("PATCH", "/articles/"+env.ID, `{"data":{"title":7}}`), 422, "update invalid")

	rr = do("GET", "/articles/options/tag", "")
	check(rr, 200, "options")
	if !strings.Contains(rr.Body.String(), `"Tag 1"`) {
		t.Errorf("options body = %s", rr.Body.String())
	}
	check(do("GET", "/articles/options/nope", ""), 404, "options unknown field")

	check(do("DELETE", "/articles/"+env.ID, ""), 204, "delete")
	check(do("GET", "/articles/"+env.ID, ""), 404, "load after delete")
}
