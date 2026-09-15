package editrig

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func configuredRouter(t *testing.T, e Entity, opts ...HandlerOption) http.Handler {
	t.Helper()
	reg, err := NewRegistry(e)
	if err != nil {
		t.Fatal(err)
	}
	return mountTest(NewHandler(reg, nil, nil, opts...), "")
}
func configRequest(r http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rr
}
func TestNilCatalogAndOptionalHooks(t *testing.T) {
	e := fullEntity("widgets")
	e.Validate = nil
	e.Schema = func(_ context.Context, cat Catalog, _ map[string]any) (json.RawMessage, json.RawMessage, error) {
		if cat.Title("title") != "title" || cat.Label("status", "ready") != "ready" {
			t.Fatal("plain labels unavailable")
		}
		return json.RawMessage(`{"type":"object"}`), json.RawMessage(`{}`), nil
	}
	r := configuredRouter(t, e)
	for _, q := range []struct {
		method, path, body string
		status             int
	}{{"GET", "/widgets/schema", "", 200}, {"POST", "/widgets", `{"data":{}}`, 201}, {"GET", "/unknown/schema", "", 400}} {
		rr := configRequest(r, q.method, q.path, q.body)
		if rr.Code != q.status {
			t.Fatalf("%s %s: %d %s", q.method, q.path, rr.Code, rr.Body)
		}
	}
}
func TestDisabledOperations(t *testing.T) {
	for _, operation := range []string{"no save", "no delete", "create", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			e := fullEntity("widgets")
			method, path := "POST", "/widgets"
			switch operation {
			case "no save":
				e.Save = nil
			case "no delete":
				e.Delete = nil
				method, path = "DELETE", "/widgets/1"
			case "create":
				e.DisableCreate = true
			case "update":
				e.DisableUpdate = true
				method, path = "PATCH", "/widgets/1"
			case "delete":
				e.DisableDelete = true
				method, path = "DELETE", "/widgets/1"
			}
			e.Load = func(context.Context, string) (map[string]any, error) {
				t.Fatal("disabled operation loaded storage")
				return nil, nil
			}
			rr := configRequest(configuredRouter(t, e), method, path, `{"data":{}}`)
			if rr.Code != 405 {
				t.Fatalf("%d %s", rr.Code, rr.Body)
			}
		})
	}
}
func TestUnknownFieldPolicy(t *testing.T) {
	for _, tc := range []struct {
		policy UnknownFieldPolicy
		status int
		keep   bool
	}{{AllowUnknownFields, 201, true}, {RejectUnknownFields, 422, false}, {StripUnknownFields, 201, false}} {
		t.Run(string(tc.policy), func(t *testing.T) {
			e := fullEntity("widgets")
			called := false
			e.Schema = func(context.Context, Catalog, map[string]any) (json.RawMessage, json.RawMessage, error) {
				return json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}}}`), json.RawMessage(`{}`), nil
			}
			e.Save = func(_ context.Context, _ *string, in map[string]any) (string, []FieldError, error) {
				called = true
				_, has := in["extra"]
				if has != tc.keep {
					t.Fatalf("payload %v", in)
				}
				return "1", nil, nil
			}
			rr := configRequest(configuredRouter(t, e, WithUnknownFields(tc.policy)), "POST", "/widgets", `{"data":{"title":"yes","extra":true}}`)
			if rr.Code != tc.status || called != (tc.status == 201) {
				t.Fatalf("%d %s saved=%v", rr.Code, rr.Body, called)
			}
		})
	}
}
func TestConfiguredJSONLimitAndTrailingData(t *testing.T) {
	r := configuredRouter(t, fullEntity("widgets"), WithBodyLimits(BodyLimits{JSONBytes: 32}))
	for _, body := range []string{`{"data":{"long":"` + strings.Repeat("x", 40) + `"}}`, `{"data":{}}` + strings.Repeat(" ", 40)} {
		rr := configRequest(r, "POST", "/widgets", body)
		if rr.Code != 413 {
			t.Fatalf("want 413: %d %s", rr.Code, rr.Body)
		}
	}
	rr := configRequest(r, "POST", "/widgets", `{"data":{}} {}`)
	if rr.Code != 400 {
		t.Fatalf("trailing JSON: %d %s", rr.Code, rr.Body)
	}
}
func TestConfiguredMultipartLimit(t *testing.T) {
	req := buildMultipartRequest(t, `{"data":{}}`, map[string]string{"file:photo": strings.Repeat("x", 2000)})
	req.URL.Path = "/widgets"
	rr := httptest.NewRecorder()
	configuredRouter(t, fullEntity("widgets"), WithBodyLimits(BodyLimits{MultipartBytes: 1024, MultipartMemory: 16})).ServeHTTP(rr, req)
	if rr.Code != 413 {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
}

func TestMultipartLimitIncludesEpilogue(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		req := buildMultipartRequest(t, `{"data":{}}`, nil)
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		for _, padding := range []int{10, 5000} {
			body := append(append([]byte{}, raw...), bytes.Repeat([]byte("x"), padding)...)
			next := httptest.NewRequest("POST", "/widgets", bytes.NewReader(body))
			next.Header = req.Header.Clone()
			if streaming {
				next.ContentLength = -1
			}
			rr := httptest.NewRecorder()
			configuredRouter(t, fullEntity("widgets"), WithBodyLimits(BodyLimits{MultipartBytes: 1024})).ServeHTTP(rr, next)
			want := 201
			if padding > 1024 {
				want = 413
			}
			if rr.Code != want {
				t.Fatalf("streaming=%v padding=%d: %d %s", streaming, padding, rr.Code, rr.Body)
			}
		}
	}
}
