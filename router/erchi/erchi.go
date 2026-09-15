// Package erchi mounts an editrig.Handler on a chi router.
package erchi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	editrig "github.com/qrotux/editrig-go"
)

// Register mounts GET base/{name}/schema, GET base/{name}/options/{field},
// GET base/{name}/{id}, POST base/{name}, PATCH base/{name}/{id} and
// DELETE base/{name}/{id}; guard may be nil.
func Register(r chi.Router, base string, guard func(http.Handler) http.Handler, h *editrig.Handler) {
	if guard == nil {
		guard = func(next http.Handler) http.Handler { return next }
	}
	r.With(guard).Get(base+"/{name}/schema", func(w http.ResponseWriter, req *http.Request) {
		h.Schema(w, req, chi.URLParam(req, "name"))
	})
	r.With(guard).Get(base+"/{name}/options/{field}", func(w http.ResponseWriter, req *http.Request) {
		h.Options(w, req, chi.URLParam(req, "name"), chi.URLParam(req, "field"))
	})
	r.With(guard).Get(base+"/{name}/{id}", func(w http.ResponseWriter, req *http.Request) {
		h.Load(w, req, chi.URLParam(req, "name"), chi.URLParam(req, "id"))
	})
	r.With(guard).Post(base+"/{name}", func(w http.ResponseWriter, req *http.Request) {
		h.Create(w, req, chi.URLParam(req, "name"))
	})
	r.With(guard).Patch(base+"/{name}/{id}", func(w http.ResponseWriter, req *http.Request) {
		h.Update(w, req, chi.URLParam(req, "name"), chi.URLParam(req, "id"))
	})
	r.With(guard).Delete(base+"/{name}/{id}", func(w http.ResponseWriter, req *http.Request) {
		h.Delete(w, req, chi.URLParam(req, "name"), chi.URLParam(req, "id"))
	})
}
