package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresExample(t *testing.T) {
	dsn := os.Getenv("EDITRIG_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("EDITRIG_TEST_DATABASE_URL is not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if h := u.Hostname(); h != "localhost" && h != "127.0.0.1" && h != "::1" {
		t.Skip("example integration test requires local PostgreSQL")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := setup(ctx, pool); err != nil {
		t.Fatal(err)
	}
	handler := newHandler(pool)
	call := func(method, path, body string, status int) map[string]any {
		t.Helper()
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(method, path, strings.NewReader(body)))
		if rr.Code != status {
			t.Fatalf("%s: %d %s", method, rr.Code, rr.Body)
		}
		var out map[string]any
		if status != 204 {
			if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	created := call("POST", "/articles", `{"data":{"title":"Example","note":"before"}}`, 201)
	id, ok := created["id"].(string)
	if !ok || id == "" {
		t.Fatalf("id: %v", created["id"])
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM editrig_example_articles WHERE id::text=$1", id)
	})
	updated := call("PATCH", "/articles/"+id, `{"data":{"note":null}}`, 200)
	data := updated["data"].(map[string]any)
	if data["title"] != "Example" || data["note"] != nil {
		t.Fatalf("%v", data)
	}
	call("DELETE", "/articles/"+id, "", 204)
	call("GET", "/articles/"+id, "", 404)
}
