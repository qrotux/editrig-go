package editrig_test

// package editrig_test, not editrig: FieldKey and SubfieldSuffix mirror the
// same catalog-key suffix from the TWO SIDES of a boundary the engine and the
// presentation vocabulary never cross by import (FieldKey in catalog.go,
// SubfieldSuffix in ui/items.go). An internal test cannot check it: the core
// does not import ui (doc.go), while an external test may import both, and
// only from this side is the whole formula visible. A drift is silent by
// construction: catalog.go never reads SubfieldSuffix, and it would surface
// only as a raw key rendered in the form.

import (
	"strings"
	"testing"

	"github.com/qrotux/editrig-go"
	"github.com/qrotux/editrig-go/ui"
)

func TestFieldKeyMirrorsSubfieldSuffix(t *testing.T) {
	got := editrig.FieldKey("e", "f", "s")
	want := "s"
	if !strings.HasSuffix(got, ui.SubfieldSuffix+want) {
		t.Errorf("FieldKey(%q,%q,%q) = %q, does not end with ui.SubfieldSuffix+sub (%q) — "+
			"the two literals mirroring each other across the editor/ui import boundary have drifted",
			"e", "f", "s", got, ui.SubfieldSuffix+want)
	}
}
