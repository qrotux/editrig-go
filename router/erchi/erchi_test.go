package erchi

import (
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/qrotux/editrig-go/router/internal/routertest"
)

func TestRoutes(t *testing.T) {
	r := chi.NewRouter()
	Register(r, "/api/admin/entities", passGuard, memEntity(t))
	routertest.Run(t, r, "/api/admin/entities")
}

func TestNilGuard(t *testing.T) {
	r := chi.NewRouter()
	Register(r, "", nil, memEntity(t))
	routertest.Run(t, r, "")
}
