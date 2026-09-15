package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExampleCRUD(t *testing.T) {
	handler := newHandler()
	call := func(method, path, body string, status int) map[string]any {
		t.Helper()
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(method, path, strings.NewReader(body)))
		if rr.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, rr.Code, rr.Body)
		}
		var out map[string]any
		if status != 204 {
			if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	schema := call(http.MethodGet, "/articles/schema", "", 200)
	if schema["id"] != nil {
		t.Fatal("create schema has an id")
	}
	created := call(http.MethodPost, "/articles", `{"data":{"title":"First","note":"before"}}`, 201)
	id, ok := created["id"].(string)
	if !ok || id == "" {
		t.Fatalf("id=%v", created["id"])
	}
	updated := call(http.MethodPatch, "/articles/"+id, `{"data":{"note":null}}`, 200)
	data := updated["data"].(map[string]any)
	if data["title"] != "First" || data["note"] != nil {
		t.Fatalf("data=%v", data)
	}
	call(http.MethodGet, "/articles/"+id, "", 200)
	call(http.MethodDelete, "/articles/"+id, "", 204)
	call(http.MethodGet, "/articles/"+id, "", 404)
}
