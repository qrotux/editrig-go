package editrig

import (
	"net/http"
	"strconv"
	"strings"
)

// Limit bounds. Default and ceiling live in the engine, not in the entity: the
// query string comes from outside, and whoever parses it must clamp it;
// otherwise every entity would re-check the number, and one that forgot would
// return the whole collection in one response.
const (
	optionsDefaultLimit = 50
	optionsMaxLimit     = 200
)

// optionsBody is the body of GET /options/{field}. A struct rather than a map:
// Options always serializes as an array (see options below), and that is part
// of the contract with the widget.
type optionsBody struct {
	Options []Option `json:"options"`
}

// Options answers GET {name}/options/{field}: the value list of a relation field.
//
// A READ handler: it does not touch the entity and the application mounts it
// next to schema/load, outside the audit wrapping of writes.
//
// The engine stays a protocol here: it parses the query string and serializes
// the response, while WHERE the values come from (Postgres, a file, an
// in-memory slice, another service) only the application's OptionSource knows
// (the core has no driver, see doc.go).
func (h *Handler) Options(w http.ResponseWriter, r *http.Request, name, field string) {
	e, ok := h.entityOf(w, name)
	if !ok {
		return
	}
	// No source is 404: an entity without relation fields and a typo in the
	// field name get the same answer because the question is the same, "no
	// such resource". The entity reports nothing for it: the absent map key
	// is the answer.
	source, ok := e.Options[field]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	opts, err := source(r.Context(), h.catalog(r, e), optionsQuery(r))
	if err != nil {
		h.log.Error("editor options", "entity", e.Name, "field", field, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "options"})
		return
	}
	if opts == nil {
		// nil serializes as null and the widget iterates the response
		// unguarded: an empty list must be an empty ARRAY.
		opts = []Option{}
	}
	writeJSON(w, http.StatusOK, optionsBody{Options: opts})
}

// optionsQuery parses the query string into an OptionsQuery. A garbage limit is
// the default, not an error: a hint "limit must be a number" helps no admin
// user, and a 400 would break the widget for nothing.
func optionsQuery(r *http.Request) OptionsQuery {
	q := r.URL.Query()

	limit := optionsDefaultLimit
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		limit = min(n, optionsMaxLimit)
	}

	var ids []string
	for _, raw := range strings.Split(q.Get("ids"), ",") {
		// Empty elements ("a,,b", a trailing comma) are dropped: reaching the
		// store, an empty string would land in the IN list and silently match
		// nothing.
		if id := strings.TrimSpace(raw); id != "" {
			ids = append(ids, id)
		}
	}

	return OptionsQuery{
		Search: strings.TrimSpace(q.Get("q")), Limit: limit, IDs: ids,
		Parent: strings.TrimSpace(q.Get("parent")),
	}
}
