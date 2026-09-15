package erstd

import (
	"net/http"
	"testing"

	"github.com/qrotux/editrig-go/router/internal/routertest"
)

func TestRoutes(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, "/api/admin/entities", passGuard, memEntity(t))
	routertest.Run(t, mux, "/api/admin/entities")
}
