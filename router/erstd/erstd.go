// Package erstd mounts an editrig.Handler on net/http's ServeMux.
package erstd

import (
	"net/http"

	editrig "github.com/qrotux/editrig-go"
)

// Register mounts GET base/{name}/schema, GET base/{name}/options/{field},
// GET base/{name}/{id}, POST base/{name}, PATCH base/{name}/{id} and
// DELETE base/{name}/{id}; guard may be nil. Requires Go 1.22 patterns.
func Register(mux *http.ServeMux, base string, guard func(http.Handler) http.Handler, h *editrig.Handler) {
	wrap := func(f func(http.ResponseWriter, *http.Request)) http.Handler {
		var hh http.Handler = http.HandlerFunc(f)
		if guard != nil {
			hh = guard(hh)
		}
		return hh
	}
	mux.Handle("GET "+base+"/{name}/schema", wrap(func(w http.ResponseWriter, r *http.Request) {
		h.Schema(w, r, r.PathValue("name"))
	}))
	mux.Handle("GET "+base+"/{name}/options/{field}", wrap(func(w http.ResponseWriter, r *http.Request) {
		h.Options(w, r, r.PathValue("name"), r.PathValue("field"))
	}))
	mux.Handle("GET "+base+"/{name}/{id}", wrap(func(w http.ResponseWriter, r *http.Request) {
		h.Load(w, r, r.PathValue("name"), r.PathValue("id"))
	}))
	mux.Handle("POST "+base+"/{name}", wrap(func(w http.ResponseWriter, r *http.Request) {
		h.Create(w, r, r.PathValue("name"))
	}))
	mux.Handle("PATCH "+base+"/{name}/{id}", wrap(func(w http.ResponseWriter, r *http.Request) {
		h.Update(w, r, r.PathValue("name"), r.PathValue("id"))
	}))
	mux.Handle("DELETE "+base+"/{name}/{id}", wrap(func(w http.ResponseWriter, r *http.Request) {
		h.Delete(w, r, r.PathValue("name"), r.PathValue("id"))
	}))
}
