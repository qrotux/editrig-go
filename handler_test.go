package editrig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeStore - in-memory backing store for the fake entity used across the
// handler tests (no DB).
type fakeStore struct {
	mu   sync.Mutex
	data map[string]map[string]any
	next int
}

func newFakeStore() *fakeStore {
	return &fakeStore{data: map[string]map[string]any{}}
}

// fakeEntity - Schema returns a SHORT schema (required:["a"]) when data==nil
// and a FULL schema (adds optional "b") otherwise; Save/Load/Delete/Validate
// operate on the in-memory store.
func fakeEntity(store *fakeStore) Entity {
	const shortSchema = `{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`
	const fullSchema = `{"type":"object","required":["a"],"properties":{"a":{"type":"string"},"b":{"type":"string"}}}`

	return Entity{
		Name: "widgets",
		Schema: func(ctx context.Context, cat Catalog, data map[string]any) (json.RawMessage, json.RawMessage, error) {
			if data == nil {
				return json.RawMessage(shortSchema), json.RawMessage(`{}`), nil
			}
			return json.RawMessage(fullSchema), json.RawMessage(`{}`), nil
		},
		Load: func(ctx context.Context, id string) (map[string]any, error) {
			store.mu.Lock()
			defer store.mu.Unlock()
			v, ok := store.data[id]
			if !ok {
				return nil, nil
			}
			cp := make(map[string]any, len(v))
			for k, vv := range v {
				cp[k] = vv
			}
			return cp, nil
		},
		Save: func(ctx context.Context, id *string, in map[string]any) (string, []FieldError, error) {
			store.mu.Lock()
			defer store.mu.Unlock()
			cp := make(map[string]any, len(in))
			for k, v := range in {
				cp[k] = v
			}
			if id == nil {
				store.next++
				newID := strconv.Itoa(store.next)
				store.data[newID] = cp
				return newID, nil, nil
			}
			store.data[*id] = cp
			return *id, nil, nil
		},
		Delete: func(ctx context.Context, id string) error {
			store.mu.Lock()
			defer store.mu.Unlock()
			delete(store.data, id)
			return nil
		},
		Validate: func(ctx context.Context, cat Catalog, id *string, in map[string]any) []FieldError {
			return nil
		},
	}
}

// testCatalog is a catalog double that returns the key itself. The core knows
// no locale (see catalog.go), so the double has none to take; it only shows
// that the engine asks for labels by the key convention.
type testCatalog struct{ entity string }

func (c testCatalog) Title(field string) string        { return Key(c.entity, field) }
func (c testCatalog) Label(field, value string) string { return ValueKey(c.entity, field, value) }
func (c testCatalog) Group(id string) string           { return GroupKey(c.entity, id) }
func (c testCatalog) Message(suffix string) string     { return Key(c.entity, suffix) }

// newTestHandler builds the Handler over one entity and returns it with a
// "write path was reached" flag: a 422 must NOT reach Save, so wrappers over
// Save and Delete set the flag.
func newTestHandler(t *testing.T, e Entity) (*Handler, *bool) {
	t.Helper()
	saved := new(bool)
	baseSave, baseDelete := e.Save, e.Delete
	e.Save = func(ctx context.Context, id *string, in map[string]any) (string, []FieldError, error) {
		*saved = true
		return baseSave(ctx, id, in)
	}
	// Delete is a write path too: TestDeleteFlow checks it was reached.
	e.Delete = func(ctx context.Context, id string) error {
		*saved = true
		return baseDelete(ctx, id)
	}
	reg, err := NewRegistry(e)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return NewHandler(reg, func(_ *http.Request, entity string) Catalog { return testCatalog{entity: entity} }, slog.New(slog.NewTextHandler(io.Discard, nil))), saved
}

// newTestRouter mounts the Handler on a ServeMux under the same paths the
// README shows; the tests exercise the wire contract, not a router.
func newTestRouter(hs *Handler) http.Handler {
	return mountTest(hs, "/api/admin/entities")
}

func mountTest(hs *Handler, base string) http.Handler {
	r := http.NewServeMux()
	r.HandleFunc("GET "+base+"/{name}/schema", func(w http.ResponseWriter, req *http.Request) {
		hs.Schema(w, req, req.PathValue("name"))
	})
	r.HandleFunc("GET "+base+"/{name}/options/{field}", func(w http.ResponseWriter, req *http.Request) {
		hs.Options(w, req, req.PathValue("name"), req.PathValue("field"))
	})
	r.HandleFunc("GET "+base+"/{name}/{id}", func(w http.ResponseWriter, req *http.Request) {
		hs.Load(w, req, req.PathValue("name"), req.PathValue("id"))
	})
	r.HandleFunc("POST "+base+"/{name}", func(w http.ResponseWriter, req *http.Request) {
		hs.Create(w, req, req.PathValue("name"))
	})
	r.HandleFunc("PATCH "+base+"/{name}/{id}", func(w http.ResponseWriter, req *http.Request) {
		hs.Update(w, req, req.PathValue("name"), req.PathValue("id"))
	})
	r.HandleFunc("DELETE "+base+"/{name}/{id}", func(w http.ResponseWriter, req *http.Request) {
		hs.Delete(w, req, req.PathValue("name"), req.PathValue("id"))
	})
	return r
}

func decodeEnvelope(t *testing.T, body []byte) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, body)
	}
	return env
}

func decodeErrBody(t *testing.T, body []byte) errBody {
	t.Helper()
	var eb errBody
	if err := json.Unmarshal(body, &eb); err != nil {
		t.Fatalf("decode errBody: %v (body=%s)", err, body)
	}
	return eb
}

func hasProperty(t *testing.T, schema json.RawMessage, prop string) bool {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(schema, &doc); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	props, _ := doc["properties"].(map[string]any)
	_, ok := props[prop]
	return ok
}

func TestSchemaHandler(t *testing.T) {
	hs, _ := newTestHandler(t, fakeEntity(newFakeStore()))
	r := newTestRouter(hs)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/entities/widgets/schema", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	env := decodeEnvelope(t, rec.Body.Bytes())
	if env.Name != "widgets" {
		t.Errorf("Name = %q", env.Name)
	}
	if env.ID != nil {
		t.Errorf("ID = %v, want nil", env.ID)
	}
	if env.Data == nil || len(env.Data) != 0 {
		t.Errorf("Data = %v, want empty map", env.Data)
	}
	if hasProperty(t, env.Schema, "b") {
		t.Errorf("schema handler should return the SHORT schema (no 'b'), got %s", env.Schema)
	}
}

func TestUnknownEntityName(t *testing.T) {
	hs, _ := newTestHandler(t, fakeEntity(newFakeStore()))
	r := newTestRouter(hs)

	paths := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/admin/entities/nope/schema"},
		{http.MethodGet, "/api/admin/entities/nope/1"},
		{http.MethodPost, "/api/admin/entities/nope"},
		{http.MethodPatch, "/api/admin/entities/nope/1"},
		{http.MethodDelete, "/api/admin/entities/nope/1"},
	}
	for _, p := range paths {
		req := httptest.NewRequest(p.method, p.path, bytes.NewReader([]byte(`{}`)))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s %s: status = %d, want 400 (body=%s)", p.method, p.path, rec.Code, rec.Body.String())
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body["error"] != "unknown entity" {
			t.Errorf("%s %s: error = %q, want %q", p.method, p.path, body["error"], "unknown entity")
		}
	}
}

func TestLoadNotFound(t *testing.T) {
	hs, _ := newTestHandler(t, fakeEntity(newFakeStore()))
	r := newTestRouter(hs)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/entities/widgets/does-not-exist", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestCreateFlow(t *testing.T) {
	hs, saved := newTestHandler(t, fakeEntity(newFakeStore()))
	r := newTestRouter(hs)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/entities/widgets", strings.NewReader(`{"data":{"a":"hello"}}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	env := decodeEnvelope(t, rec.Body.Bytes())
	if env.ID == nil || *env.ID == "" {
		t.Fatalf("expected non-nil/non-empty ID, got %v", env.ID)
	}
	if !hasProperty(t, env.Schema, "b") {
		t.Errorf("create response should carry the FULL schema (has 'b'), got %s", env.Schema)
	}
	if env.Data["a"] != "hello" {
		t.Errorf("Data[a] = %v, want hello", env.Data["a"])
	}
	if !*saved {
		t.Errorf("expected the entity write path to be reached")
	}

	// Load it back to confirm Save actually persisted.
	req2 := httptest.NewRequest(http.MethodGet, "/api/admin/entities/widgets/"+*env.ID, nil)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("reload status = %d, want 200", rec2.Code)
	}
}

func TestCreateValidationError(t *testing.T) {
	hs, saved := newTestHandler(t, fakeEntity(newFakeStore()))
	r := newTestRouter(hs)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/entities/widgets", strings.NewReader(`{"data":{}}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body=%s)", rec.Code, rec.Body.String())
	}
	eb := decodeErrBody(t, rec.Body.Bytes())
	if len(eb.FieldErrors) != 1 || eb.FieldErrors[0].Field != "/a" {
		t.Fatalf("FieldErrors = %+v, want one entry for /a", eb.FieldErrors)
	}
	if len(eb.FormErrors) != 0 {
		t.Errorf("FormErrors = %v, want empty", eb.FormErrors)
	}
	if *saved {
		t.Errorf("Save must not be reached on a 422")
	}
}

func TestUpdateFlow(t *testing.T) {
	hs, saved := newTestHandler(t, fakeEntity(newFakeStore()))
	r := newTestRouter(hs)

	// Seed via create.
	createReq := httptest.NewRequest(http.MethodPost, "/api/admin/entities/widgets", strings.NewReader(`{"data":{"a":"hello"}}`))
	createRec := httptest.NewRecorder()
	r.ServeHTTP(createRec, createReq)
	env := decodeEnvelope(t, createRec.Body.Bytes())
	id := *env.ID

	*saved = false // reset before the update we're actually asserting on

	req := httptest.NewRequest(http.MethodPatch, "/api/admin/entities/widgets/"+id, strings.NewReader(`{"data":{"a":"updated","b":"extra"}}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	updated := decodeEnvelope(t, rec.Body.Bytes())
	if updated.Data["a"] != "updated" || updated.Data["b"] != "extra" {
		t.Errorf("Data = %v, want replaced fields", updated.Data)
	}
	if !*saved {
		t.Errorf("expected the entity write path to be reached")
	}
}

func TestUpdateNotFound(t *testing.T) {
	hs, _ := newTestHandler(t, fakeEntity(newFakeStore()))
	r := newTestRouter(hs)

	req := httptest.NewRequest(http.MethodPatch, "/api/admin/entities/widgets/does-not-exist", strings.NewReader(`{"data":{"a":"x"}}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestSaveErrNotFoundIs404 pins the race: the row existed at the preliminary
// Load and is gone by Save. It is the only path on which ErrNotFound from the
// store reaches the handler, and it must become 404, not a 422 "not found"
// validation error.
//
// The entity is faked so Load finds the row and Save always reports it
// missing: a real race cannot be reproduced in a test, and what is checked is
// the sentinel-to-status translation.
func TestSaveErrNotFoundIs404(t *testing.T) {
	store := newFakeStore()
	e := fakeEntity(store)
	e.Save = func(ctx context.Context, id *string, in map[string]any) (string, []FieldError, error) {
		return "", nil, ErrNotFound
	}
	hs, _ := newTestHandler(t, e)
	r := newTestRouter(hs)

	// Seed the row directly in the store so the preliminary Load finds it and
	// the path reaches Save.
	store.data["id-1"] = map[string]any{"a": "x"}

	req := httptest.NewRequest(http.MethodPatch, "/api/admin/entities/widgets/id-1", strings.NewReader(`{"data":{"a":"y"}}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestDeleteFlow(t *testing.T) {
	hs, saved := newTestHandler(t, fakeEntity(newFakeStore()))
	r := newTestRouter(hs)

	createReq := httptest.NewRequest(http.MethodPost, "/api/admin/entities/widgets", strings.NewReader(`{"data":{"a":"hello"}}`))
	createRec := httptest.NewRecorder()
	r.ServeHTTP(createRec, createReq)
	env := decodeEnvelope(t, createRec.Body.Bytes())
	id := *env.ID

	*saved = false

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/entities/widgets/"+id, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("expected empty body on 204, got %q", rec.Body.String())
	}
	if !*saved {
		t.Errorf("expected the entity write path to be reached")
	}

	// Confirm it's gone.
	loadReq := httptest.NewRequest(http.MethodGet, "/api/admin/entities/widgets/"+id, nil)
	loadRec := httptest.NewRecorder()
	r.ServeHTTP(loadRec, loadReq)
	if loadRec.Code != http.StatusNotFound {
		t.Fatalf("post-delete load status = %d, want 404", loadRec.Code)
	}

	// Deleting again is a 404 (Load short-circuits before Delete/tx).
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, httptest.NewRequest(http.MethodDelete, "/api/admin/entities/widgets/"+id, nil))
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("re-delete status = %d, want 404", rec2.Code)
	}
}

// TestRemoveConflictReturns409: Entity.Delete refuses by data state
// (ConflictError), not by shape, and the handler must answer 409 with the
// JOINED string (cat.Message(Key) + ": " + the cat.Message(LabelKey)+" "+Count
// entries joined by ", "), not a bare 500. testCatalog returns the key itself
// per the editor.<entity>.<suffix> convention (catalog.go), enough to check
// the join without depending on a real application catalog.
func TestRemoveConflictReturns409(t *testing.T) {
	store := newFakeStore()
	store.data["id-1"] = map[string]any{"a": "x"}
	e := fakeEntity(store)
	e.Delete = func(context.Context, string) error {
		return &ConflictError{Key: "delete_blocked", Refs: []ConflictRef{
			{LabelKey: "refs.users", Count: 342},
			{LabelKey: "refs.trips", Count: 87},
		}}
	}
	hs, _ := newTestHandler(t, e)
	r := newTestRouter(hs)

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/entities/widgets/id-1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	eb := decodeErrBody(t, rec.Body.Bytes())
	if len(eb.FormErrors) != 1 {
		t.Fatalf("FormErrors = %v, want exactly one", eb.FormErrors)
	}
	want := "editor.widgets.delete_blocked: editor.widgets.refs.users 342, editor.widgets.refs.trips 87"
	if got := eb.FormErrors[0]; got != want {
		t.Errorf("FormErrors[0] = %q, want %q", got, want)
	}
	if eb.FieldErrors == nil {
		t.Error("FieldErrors must be [], not null")
	}
}

// TestRemoveOtherErrorStays500: not every Delete error is a conflict; an
// ordinary one (a driver failure, say) still gives 500, not 409.
func TestRemoveOtherErrorStays500(t *testing.T) {
	store := newFakeStore()
	store.data["id-1"] = map[string]any{"a": "x"}
	e := fakeEntity(store)
	e.Delete = func(context.Context, string) error { return errors.New("boom") }
	hs, _ := newTestHandler(t, e)
	r := newTestRouter(hs)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/admin/entities/widgets/id-1", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body=%s)", rec.Code, rec.Body.String())
	}
}

// mediaEntity is the fake entity of the multipart write-path tests: "username"
// (plain), "photo_id" (ui:field=media, single), "locked_photo_id" (media AND
// ui:readonly=true, to check that read-only cuts files like plain values) and
// "gallery" (media with multi=true, appending files to the tail of the id
// array). Nothing is required: these tests exercise the multipart plumbing,
// not structural validation.
func mediaEntity(store *fakeStore) Entity {
	const schema = `{"type":"object","properties":{"username":{"type":"string"},"photo_id":{"type":"string"},"locked_photo_id":{"type":"string"},"gallery":{"type":"array","items":{"type":"string"}}}}`
	const uiSchema = `{"photo_id":{"ui:field":"media"},"locked_photo_id":{"ui:field":"media","ui:readonly":true},"gallery":{"ui:field":"media","ui:options":{"multi":true}}}`

	return Entity{
		Name: "users",
		Schema: func(ctx context.Context, cat Catalog, data map[string]any) (json.RawMessage, json.RawMessage, error) {
			return json.RawMessage(schema), json.RawMessage(uiSchema), nil
		},
		Load: func(ctx context.Context, id string) (map[string]any, error) {
			store.mu.Lock()
			defer store.mu.Unlock()
			v, ok := store.data[id]
			if !ok {
				return nil, nil
			}
			cp := make(map[string]any, len(v))
			for k, vv := range v {
				cp[k] = vv
			}
			return cp, nil
		},
		Save: func(ctx context.Context, id *string, in map[string]any) (string, []FieldError, error) {
			store.mu.Lock()
			defer store.mu.Unlock()
			store.next++
			newID := strconv.Itoa(store.next)
			store.data[newID] = in
			return newID, nil, nil
		},
		Delete:   func(ctx context.Context, id string) error { return nil },
		Validate: func(ctx context.Context, cat Catalog, id *string, in map[string]any) []FieldError { return nil },
	}
}

// doRequest builds the handler and router for e (through newTestHandler) and
// runs one request; shared by the multipart and JSON write-path tests.
func doRequest(t *testing.T, e Entity, method, path string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	hs, _ := newTestHandler(t, e)
	r := newTestRouter(hs)
	req := httptest.NewRequest(method, path, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// multipartBody builds a multipart/form-data body: a "payload" part (raw JSON,
// omitted when payloadJSON=="", which
// TestWriteMultipartWithoutPayloadPartIsEmptyInput relies on) and one
// "file:<field>" part. Returns the body and the Content-Type value (with
// boundary) that doRequest expects.
func multipartBody(t *testing.T, payloadJSON, filePartName, filename, mime string, data []byte) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if payloadJSON != "" {
		if err := mw.WriteField("payload", payloadJSON); err != nil {
			t.Fatalf("write payload field: %v", err)
		}
	}
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="`+filePartName+`"; filename="`+filename+`"`)
	hdr.Set("Content-Type", mime)
	part, err := mw.CreatePart(hdr)
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

// TestWriteAcceptsMultipartAndDeliversUploadToSave is the happy path: the
// "payload" part carries the JSON data, "file:photo_id" a file for the media
// field; both reach Save merged into one map, the file as an Upload.
func TestWriteAcceptsMultipartAndDeliversUploadToSave(t *testing.T) {
	var got map[string]any
	e := mediaEntity(newFakeStore())
	e.Save = func(_ context.Context, _ *string, in map[string]any) (string, []FieldError, error) {
		got = in
		return "new-id", nil, nil
	}

	body, ct := multipartBody(t, `{"data":{"username":"u"}}`, "file:photo_id", "p.jpg", "image/jpeg", []byte("\xff\xd8\xff"))
	rr := doRequest(t, e, http.MethodPost, "/api/admin/entities/users", body, ct)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rr.Code, rr.Body)
	}
	up, ok := got["photo_id"].(Upload)
	if !ok {
		t.Fatalf("photo_id = %T, want editor.Upload", got["photo_id"])
	}
	if up.Mime != "image/jpeg" || up.Filename != "p.jpg" || len(up.Data) == 0 {
		t.Errorf("upload = %+v, want the submitted part", up)
	}
	if got["username"] != "u" {
		t.Errorf("payload part lost: %v", got)
	}
}

// multipartBodyMulti is multipartBody with several file parts (repeated names
// included): a multi-media field sends one part per file.
func multipartBodyMulti(t *testing.T, payloadJSON string, parts []struct {
	Name, Filename, Mime string
	Data                 []byte
}) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if payloadJSON != "" {
		if err := mw.WriteField("payload", payloadJSON); err != nil {
			t.Fatalf("write payload field: %v", err)
		}
	}
	for _, p := range parts {
		hdr := textproto.MIMEHeader{}
		hdr.Set("Content-Disposition", `form-data; name="`+p.Name+`"; filename="`+p.Filename+`"`)
		hdr.Set("Content-Type", p.Mime)
		part, err := mw.CreatePart(hdr)
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		if _, err := part.Write(p.Data); err != nil {
			t.Fatalf("write file part: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

// TestWriteRejectsTwoFilesOnSingleMediaField: a single media field takes
// EXACTLY one file; a second part is an error, not a silently ignored file (a
// lost file is indistinguishable from success).
func TestWriteRejectsTwoFilesOnSingleMediaField(t *testing.T) {
	e := mediaEntity(newFakeStore())
	saveCalled := false
	e.Save = func(_ context.Context, _ *string, in map[string]any) (string, []FieldError, error) {
		saveCalled = true
		return "new-id", nil, nil
	}

	body, ct := multipartBodyMulti(t, `{"data":{"username":"u"}}`, []struct {
		Name, Filename, Mime string
		Data                 []byte
	}{
		{"file:photo_id", "a.jpg", "image/jpeg", []byte("\xff\xd8\xff")},
		{"file:photo_id", "b.jpg", "image/jpeg", []byte("\xff\xd8\xfe")},
	})
	rr := doRequest(t, e, http.MethodPost, "/api/admin/entities/users", body, ct)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rr.Code, rr.Body)
	}
	eb := decodeErrBody(t, rr.Body.Bytes())
	if len(eb.FieldErrors) != 1 || eb.FieldErrors[0].Field != "/photo_id" {
		t.Fatalf("FieldErrors = %+v, want one entry for /photo_id", eb.FieldErrors)
	}
	if saveCalled {
		t.Errorf("Save must not be reached when a single media field gets two files")
	}
}

// TestWriteAppendsFilesAfterExistingIDs: multi-media files are appended to the
// TAIL of the id array that came in the payload.
func TestWriteAppendsFilesAfterExistingIDs(t *testing.T) {
	var got map[string]any
	e := mediaEntity(newFakeStore())
	e.Save = func(_ context.Context, _ *string, in map[string]any) (string, []FieldError, error) {
		got = in
		return "new-id", nil, nil
	}

	body, ct := multipartBodyMulti(t, `{"data":{"gallery":["id-1"]}}`, []struct {
		Name, Filename, Mime string
		Data                 []byte
	}{
		{"file:gallery", "a.jpg", "image/jpeg", []byte("aaa")},
		{"file:gallery", "b.jpg", "image/jpeg", []byte("bbb")},
	})
	rr := doRequest(t, e, http.MethodPost, "/api/admin/entities/users", body, ct)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rr.Code, rr.Body)
	}
	list, ok := got["gallery"].([]any)
	if !ok || len(list) != 3 {
		t.Fatalf("gallery = %#v, want []any of len 3", got["gallery"])
	}
	if list[0] != "id-1" {
		t.Errorf("gallery[0] = %v, want the existing id \"id-1\"", list[0])
	}
	up0, ok0 := list[1].(Upload)
	up1, ok1 := list[2].(Upload)
	if !ok0 || !ok1 {
		t.Fatalf("gallery tail = %T/%T, want two editor.Upload", list[1], list[2])
	}
	if string(up0.Data) != "aaa" || string(up1.Data) != "bbb" {
		t.Errorf("gallery uploads = %q/%q, want aaa/bbb in send order", up0.Data, up1.Data)
	}
}

// TestWriteRejectsFileOnNonMediaField: "username" is not declared
// ui:field=media, so a file on it is a 422 with a FieldError, not silent
// acceptance (otherwise any string field would become a file endpoint).
func TestWriteRejectsFileOnNonMediaField(t *testing.T) {
	e := mediaEntity(newFakeStore())
	saveCalled := false
	e.Save = func(_ context.Context, _ *string, in map[string]any) (string, []FieldError, error) {
		saveCalled = true
		return "new-id", nil, nil
	}

	body, ct := multipartBody(t, `{"data":{"username":"u"}}`, "file:username", "p.jpg", "image/jpeg", []byte("\xff\xd8\xff"))
	rr := doRequest(t, e, http.MethodPost, "/api/admin/entities/users", body, ct)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rr.Code, rr.Body)
	}
	eb := decodeErrBody(t, rr.Body.Bytes())
	if len(eb.FieldErrors) != 1 || eb.FieldErrors[0].Field != "/username" {
		t.Fatalf("FieldErrors = %+v, want one entry for /username", eb.FieldErrors)
	}
	if saveCalled {
		t.Errorf("Save must not be reached when a file lands on a non-media field")
	}
}

// TestWriteRejectsFileOnReadonlyField: "locked_photo_id" is media AND
// read-only; the invariant "Entity.Save physically never sees a read-only key"
// (handler.go) must hold for file parts, not only for plain values.
func TestWriteRejectsFileOnReadonlyField(t *testing.T) {
	e := mediaEntity(newFakeStore())
	saveCalled := false
	e.Save = func(_ context.Context, _ *string, in map[string]any) (string, []FieldError, error) {
		saveCalled = true
		return "new-id", nil, nil
	}

	body, ct := multipartBody(t, `{"data":{"username":"u"}}`, "file:locked_photo_id", "p.jpg", "image/jpeg", []byte("\xff\xd8\xff"))
	rr := doRequest(t, e, http.MethodPost, "/api/admin/entities/users", body, ct)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rr.Code, rr.Body)
	}
	eb := decodeErrBody(t, rr.Body.Bytes())
	if len(eb.FieldErrors) != 1 || eb.FieldErrors[0].Field != "/locked_photo_id" {
		t.Fatalf("FieldErrors = %+v, want one entry for /locked_photo_id", eb.FieldErrors)
	}
	if saveCalled {
		t.Errorf("Save must not be reached when a file lands on a read-only field")
	}
}

// TestWriteRejectsOversizedBody: a body larger than uploadMaxBytes is cut by
// http.MaxBytesReader (413) and Save is not called. The bound sits on the read
// itself, not on ParseMultipartForm(maxMemory), or a file part beyond that
// threshold would go to a temp file with no limit (see upload.go).
func TestWriteRejectsOversizedBody(t *testing.T) {
	e := mediaEntity(newFakeStore())
	saveCalled := false
	e.Save = func(_ context.Context, _ *string, in map[string]any) (string, []FieldError, error) {
		saveCalled = true
		return "new-id", nil, nil
	}

	oversized := make([]byte, uploadMaxBytes+1)
	body, ct := multipartBody(t, `{"data":{"username":"u"}}`, "file:photo_id", "big.bin", "application/octet-stream", oversized)
	rr := doRequest(t, e, http.MethodPost, "/api/admin/entities/users", body, ct)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %s)", rr.Code, rr.Body)
	}
	if saveCalled {
		t.Errorf("Save must not be reached when the body exceeds uploadMaxBytes")
	}
}

// TestWriteRejectsOversizedBodyWithConstantMessage: the 413 carries the
// CONSTANT text ("body too large"), not the parser's err.Error(); the raw
// mime/multipart message is a Go implementation detail not to be shown to the
// client.
func TestWriteRejectsOversizedBodyWithConstantMessage(t *testing.T) {
	e := mediaEntity(newFakeStore())
	oversized := make([]byte, uploadMaxBytes+1)
	body, ct := multipartBody(t, `{"data":{"username":"u"}}`, "file:photo_id", "big.bin", "application/octet-stream", oversized)
	rr := doRequest(t, e, http.MethodPost, "/api/admin/entities/users", body, ct)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %s)", rr.Code, rr.Body)
	}
	var got map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["error"] != "body too large" {
		t.Errorf("error = %q, want the constant message %q, not the raw parser text", got["error"], "body too large")
	}
}

// TestWriteMalformedMultipartIs400: a body under uploadMaxBytes with a broken
// boundary (Content-Type names a boundary the body lacks) is a parse error,
// NOT oversize; MaxBytesReader is not involved, and the answer must be 400
// with the constant message, not 413 (a 413 would lie about the cause).
func TestWriteMalformedMultipartIs400(t *testing.T) {
	e := mediaEntity(newFakeStore())
	saveCalled := false
	e.Save = func(_ context.Context, _ *string, in map[string]any) (string, []FieldError, error) {
		saveCalled = true
		return "new-id", nil, nil
	}

	// Content-Type declares multipart with a boundary the body lacks; Go's
	// mime/multipart rejects it before reading a single body byte that
	// MaxBytesReader could count against the limit.
	body := strings.NewReader("not a multipart body at all")
	rr := doRequest(t, e, http.MethodPost, "/api/admin/entities/users", body, "multipart/form-data; boundary=missing")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rr.Code, rr.Body)
	}
	var got map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["error"] != "malformed multipart body" {
		t.Errorf("error = %q, want the constant message %q, not the raw parser text", got["error"], "malformed multipart body")
	}
	if saveCalled {
		t.Errorf("Save must not be reached on a malformed body")
	}
}

// TestWriteMultipartWithoutPayloadPartIsEmptyInput: a submit without a
// "payload" part (file only) is legitimate (a form with no plain fields), not
// a 500; in starts as an empty map and the file still arrives.
func TestWriteMultipartWithoutPayloadPartIsEmptyInput(t *testing.T) {
	var got map[string]any
	e := mediaEntity(newFakeStore())
	e.Save = func(_ context.Context, _ *string, in map[string]any) (string, []FieldError, error) {
		got = in
		return "new-id", nil, nil
	}

	body, ct := multipartBody(t, "", "file:photo_id", "p.jpg", "image/jpeg", []byte("\xff\xd8\xff"))
	rr := doRequest(t, e, http.MethodPost, "/api/admin/entities/users", body, ct)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, not a 500 on a missing payload part (body %s)", rr.Code, rr.Body)
	}
	if got == nil {
		t.Fatal("Save was not reached")
	}
	if _, ok := got["photo_id"].(Upload); !ok {
		t.Errorf("photo_id = %T, want editor.Upload even without a payload part", got["photo_id"])
	}
	if len(got) != 1 {
		t.Errorf("in = %v, want only the injected file field (no payload part means no other keys)", got)
	}
}

// TestWriteJSONPathUnchanged: a Content-Type other than multipart/* takes the
// json.Decode branch; decodeBody must not change the behaviour of plain JSON
// clients.
func TestWriteJSONPathUnchanged(t *testing.T) {
	hs, saved := newTestHandler(t, fakeEntity(newFakeStore()))
	r := newTestRouter(hs)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/entities/widgets", strings.NewReader(`{"data":{"a":"hello"}}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	env := decodeEnvelope(t, rec.Body.Bytes())
	if env.Data["a"] != "hello" {
		t.Errorf("Data[a] = %v, want hello", env.Data["a"])
	}
	if !*saved {
		t.Errorf("expected the entity write path to be reached")
	}
}

func TestMalformedJSONBody(t *testing.T) {
	hs, _ := newTestHandler(t, fakeEntity(newFakeStore()))
	r := newTestRouter(hs)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/entities/widgets", strings.NewReader(`{not-json`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}
