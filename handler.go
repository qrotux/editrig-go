package editrig

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/qrotux/editrig-go/validate"
)

// envelope is the body of GET /schema, GET /entity/{id} and the create/update
// responses.
type envelope struct {
	Name     string          `json:"name"`
	ID       *string         `json:"id"`
	Schema   json.RawMessage `json:"schema"`
	UISchema json.RawMessage `json:"uiSchema"`
	Data     map[string]any  `json:"data"`
}

// errBody is the 422 body: FieldError values split into field-local and
// form-level (Field=="") errors.
type errBody struct {
	FieldErrors []FieldError `json:"fieldErrors"`
	FormErrors  []string     `json:"formErrors"`
}

// renderConflict is the only place where conflict keys become a string. The
// comma join is locale-neutral; the words come from the application's catalog.
func renderConflict(cat Catalog, ce *ConflictError) string {
	msg := cat.Message(ce.Key)
	if len(ce.Refs) == 0 {
		return msg
	}
	parts := make([]string, 0, len(ce.Refs))
	for _, r := range ce.Refs {
		parts = append(parts, cat.Message(r.LabelKey)+" "+strconv.Itoa(r.Count))
	}
	return msg + ": " + strings.Join(parts, ", ")
}

func partition(fe []FieldError) errBody {
	b := errBody{FieldErrors: []FieldError{}, FormErrors: []string{}}
	for _, f := range fe {
		if f.Field == "" {
			b.FormErrors = append(b.FormErrors, f.Message)
		} else {
			b.FieldErrors = append(b.FieldErrors, f)
		}
	}
	return b
}

// requestBody is the POST/PATCH body: {"data": {...}}.
type requestBody struct {
	Data map[string]any `json:"data"`
}

// Handler serves the six operations of every registered entity. Its methods
// take the route parameters explicitly and know no router: a mount in
// router/erchi or router/erstd, or a few lines on any mux, reads {name},
// {id} and {field} its own way and calls them.
type Handler struct {
	reg           *Registry
	copy          Copy
	log           *slog.Logger
	unknownFields UnknownFieldPolicy
	bodyLimits    BodyLimits
}

// NewHandler builds the handler over reg; nil copy means PlainCatalog, nil log
// means slog.Default. Mounting is the caller's (see doc.go).
func NewHandler(reg *Registry, copy Copy, log *slog.Logger, options ...HandlerOption) *Handler {
	if reg == nil {
		panic("editrig: nil registry")
	}
	if copy == nil {
		copy = func(*http.Request, string) Catalog { return PlainCatalog{} }
	}
	if log == nil {
		log = slog.Default()
	}
	h := &Handler{reg: reg, copy: copy, log: log, unknownFields: AllowUnknownFields, bodyLimits: (BodyLimits{}).defaults()}
	for _, option := range options {
		option(h)
	}
	return h
}

// catalog is the entity's catalog for this request. The APPLICATION builds it
// (Copy); the engine substitutes only the name from Entity.Name, so the two
// cannot drift (see catalog.go).
func (h *Handler) catalog(r *http.Request, e *Entity) Catalog {
	return h.copy(r, e.Name)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// entityOf resolves the {name} route param, 400 {"error":"unknown entity"}
// if it does not name a registered Entity.
func (h *Handler) entityOf(w http.ResponseWriter, name string) (*Entity, bool) {
	e, ok := h.reg.Get(name)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown entity"})
		return nil, false
	}
	return e, true
}

func (h *Handler) buildEnvelope(ctx context.Context, e *Entity, cat Catalog, id *string, data map[string]any) (envelope, int, error) {
	sch, ui, err := e.Schema(ctx, cat, data)
	if err != nil {
		return envelope{}, http.StatusInternalServerError, err
	}
	return envelope{Name: e.Name, ID: id, Schema: sch, UISchema: ui, Data: data}, http.StatusOK, nil
}

// Schema answers GET {name}/schema: the create form (Schema(...,nil)), id:null, data:{}.
func (h *Handler) Schema(w http.ResponseWriter, r *http.Request, name string) {
	e, ok := h.entityOf(w, name)
	if !ok {
		return
	}
	// e.Schema gets a literal nil data (the create-form branch of the
	// Entity.Schema contract), but the envelope's Data must still render as
	// `{}`, not `null`, so this bypasses buildEnvelope, which would thread
	// the same map into both.
	sch, ui, err := e.Schema(r.Context(), h.catalog(r, e), nil)
	if err != nil {
		h.log.Error("editor schema", "entity", e.Name, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "schema"})
		return
	}
	writeJSON(w, http.StatusOK, envelope{Name: e.Name, ID: nil, Schema: sch, UISchema: ui, Data: map[string]any{}})
}

// Load answers GET {name}/{id}: Load → 404 on (nil,nil); else the full envelope.
func (h *Handler) Load(w http.ResponseWriter, r *http.Request, name, id string) {
	e, ok := h.entityOf(w, name)
	if !ok {
		return
	}
	ctx := r.Context()
	data, err := e.Load(ctx, id)
	if err != nil {
		h.log.Error("editor load", "entity", e.Name, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load"})
		return
	}
	if data == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	env, _, err := h.buildEnvelope(ctx, e, h.catalog(r, e), &id, data)
	if err != nil {
		h.log.Error("editor load schema", "entity", e.Name, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "schema"})
		return
	}
	writeJSON(w, http.StatusOK, env)
}

// Create answers POST {name}: 201 plus the envelope of the created row.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request, name string) {
	h.write(w, r, name, nil)
}

// Update answers PATCH {name}/{id}: 200 plus the envelope of the updated row;
// 404 when the row does not exist.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request, name, id string) {
	h.write(w, r, name, &id)
}

// write is the SHARED write path of create and update: one function instead of
// two near-identical ones with nothing to differ by.
//
// Exactly four differences, all below: the preliminary Load (update only),
// create form versus edit form (both Schema(...), differing only in the data
// argument), Save(nil) versus Save(&id), 201 versus 200.
func (h *Handler) write(w http.ResponseWriter, r *http.Request, name string, id *string) {
	e, ok := h.entityOf(w, name)
	if !ok {
		return
	}
	isUpdate := id != nil
	if e.Save == nil || (isUpdate && e.DisableUpdate) || (!isUpdate && e.DisableCreate) {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	cat := h.catalog(r, e)
	ctx := r.Context()

	op := "create"
	// current is the row's present state: on update it both proves existence
	// (404) and goes into Schema(data!=nil), the edit form. On create there is
	// none: Schema(...,nil) builds the create form.
	var current map[string]any
	if isUpdate {
		op = "update"
		loaded, err := e.Load(ctx, *id)
		if err != nil {
			h.log.Error("editor "+op+" load", "entity", e.Name, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load"})
			return
		}
		if loaded == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		current = loaded
	}

	in, files, cleanup, decodeStatus, err := decodeBody(w, r, h.bodyLimits)
	if err != nil {
		writeJSON(w, decodeStatus, map[string]string{"error": err.Error()})
		return
	}
	defer cleanup()
	if in == nil {
		in = map[string]any{}
	}

	sch, uiSchema, err := e.Schema(ctx, cat, current)
	if err != nil {
		h.log.Error("editor "+op+" schema", "entity", e.Name, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "schema"})
		return
	}
	// Read-only enforcement lives in the engine, not in the entity: declared
	// read-only keys are cut BEFORE validation and Save, so Entity.Save
	// physically never sees them (validate.StripReadonly).
	if dropped := validate.StripReadonly(uiSchema, in); len(dropped) > 0 {
		h.log.Debug("editor "+op+" dropped readonly fields", "entity", e.Name, "fields", dropped)
	}

	unknownErrors, err := validate.UnknownFields(sch, in, h.unknownFields)
	if err != nil {
		h.log.Error("editor schema", "err", err)
		writeJSON(w, 500, map[string]string{"error": "schema"})
		return
	}
	if len(unknownErrors) > 0 {
		writeJSON(w, 422, partition(unknownErrors))
		return
	}

	// Validation sees pending files and the effective PATCH state without
	// exposing loaded values or validation markers to the write hooks.
	counts := make(map[string]int, len(files))
	for field, ups := range files {
		counts[field] = len(ups)
	}
	structFerr, err := validate.Write(sch, uiSchema, current, in, counts)
	if err != nil {
		h.log.Error("editor "+op+" structural", "entity", e.Name, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "schema"})
		return
	}
	ferr := append([]FieldError{}, structFerr...)

	// Files go in AFTER structural validation and BEFORE Validate: injecting
	// them after Validate would skip the mime/size checks and carry anything
	// into the transaction. Field names are walked sorted because map order is
	// random and ferr ends up in the response body: unsorted, a test with two
	// stray file parts would flicker in FieldError order.
	fields := make([]string, 0, len(files))
	for field := range files {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		ups := files[field]
		switch kind := validate.MediaFieldKind(uiSchema, field); {
		case kind == validate.MediaNone, kind == validate.MediaSingle && len(ups) > 1:
			// An extra part on a single field is an error, not a silent skip: a
			// lost file is indistinguishable from a successful save.
			ferr = append(ferr, FieldError{Field: "/" + field, Message: "unexpected file"})
		case kind == validate.MediaSingle:
			in[field] = ups[0]
		default:
			// New files are appended to the TAIL of the id list from the
			// payload: the array itself expresses order and removal of existing
			// items, insertion in the middle it does not (reorder with the next
			// Save).
			list, _ := in[field].([]any)
			next := make([]any, 0, len(list)+len(ups))
			next = append(next, list...)
			for _, up := range ups {
				next = append(next, up)
			}
			in[field] = next
		}
	}

	if e.Validate != nil {
		ferr = append(ferr, e.Validate(ctx, cat, id, in)...)
	}
	if len(ferr) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, partition(ferr))
		return
	}

	outID, saveFerr, err := e.Save(ctx, id, in)
	if len(saveFerr) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, partition(saveFerr))
		return
	}
	// No row is 404, not 500 and not 422. On update the regular path catches
	// it earlier with the preliminary Load; what lands here is a race, the row
	// deleted BETWEEN Load and Save.
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		h.log.Error("editor "+op+" save", "entity", e.Name, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save"})
		return
	}
	if !isUpdate {
		id = &outID // on create the id exists only from here on
	}

	data, err := e.Load(ctx, *id)
	if err != nil {
		h.log.Error("editor "+op+" reload", "entity", e.Name, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load"})
		return
	}
	env, _, err := h.buildEnvelope(ctx, e, cat, id, data)
	if err != nil {
		h.log.Error("editor "+op+" envelope", "entity", e.Name, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "schema"})
		return
	}
	status := http.StatusCreated
	if isUpdate {
		status = http.StatusOK
	}
	writeJSON(w, status, env)
}

// Delete answers DELETE {name}/{id}: Load → 404; otherwise Delete → 204 (no
// body). The entity opens its own transaction if it needs one.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request, name, id string) {
	e, ok := h.entityOf(w, name)
	if !ok {
		return
	}
	if e.Delete == nil || e.DisableDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ctx := r.Context()

	loaded, err := e.Load(ctx, id)
	if err != nil {
		h.log.Error("editor delete load", "entity", e.Name, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load"})
		return
	}
	if loaded == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	if err := e.Delete(ctx, id); err != nil {
		var ce *ConflictError
		if errors.As(err, &ce) {
			writeJSON(w, http.StatusConflict, errBody{
				FieldErrors: []FieldError{},
				FormErrors:  []string{renderConflict(h.catalog(r, e), ce)},
			})
			return
		}
		h.log.Error("editor delete", "entity", e.Name, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
