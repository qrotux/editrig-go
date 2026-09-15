package decl

import (
	"strings"
	"testing"

	"github.com/qrotux/editrig-go/ui"
)

// wcell is a cell that answers about writability (the decl.Cell interface).
// Separate from cell in decl_test.go, which exists to prove the declaration
// itself needs no interface.
type wcell struct{ writable bool }

func (c wcell) Writable() bool { return c.writable }

// Accepts returns true for everything: every test in this file except
// TestLintChecksTheTypeAxis checks something OTHER than the type axis, and
// wcell must not trip on it by accident. "Any shape" is the same degenerate
// case as a read-only cell (KindAny).
func (c wcell) Accepts(ui.Wire) bool { return true }

func rw() wcell { return wcell{writable: true} }
func ro() wcell { return wcell{} }

// wireCell is a writable cell with a given wire shape, the implementation
// cellOf returns; the type-axis tests below use it instead of the erjet
// driver.
type wireCell struct{ wire ui.Wire }

func (c wireCell) Writable() bool         { return true }
func (c wireCell) Accepts(w ui.Wire) bool { return c.wire.Match(w) }

// cellOf returns a fakeCell (decl_test.go) that writes and accepts exactly the
// shape w.
func cellOf(w ui.Wire) fakeCell { return wireCell{wire: w} }

// readonlyCell is a cell without write access. Accepts takes part in no test
// (writable=false short-circuits the type axis before it is called), but the
// method must exist because decl.Cell requires both.
type readonlyCell struct{}

func (readonlyCell) Writable() bool       { return false }
func (readonlyCell) Accepts(ui.Wire) bool { return true }

// readonlyMismatch does not write and DELIBERATELY rejects every shape. Real
// read-only cells (erjet.ReadOnly/JSON) never lie in Accepts, their wire is
// KindAny, which Match accepts unconditionally, so they cannot exercise the
// `if writable` guard of the type axis: a test would stay green without the
// guard. This cell hits the guard directly: Accepts=false must go unnoticed
// precisely because writable=false.
type readonlyMismatch struct{}

func (readonlyMismatch) Writable() bool       { return false }
func (readonlyMismatch) Accepts(ui.Wire) bool { return false }

// sound is a clean declaration: two sections, both used, required fields
// writable, read-only fields marked read-only, the hidden field outside
// sections.
func sound() Set[wcell] {
	return Set[wcell]{
		{Name: "id", Col: ro(), UI: ui.String().Hidden().Readonly()},
		{Name: "username", Group: "identity", Col: rw(), UI: ui.String().Required().MaxLen(50)},
		{Name: "city", Group: "profile", Col: rw(), UI: ui.String().Nullable()},
		{Name: "rating", Group: "profile", Col: ro(), UI: ui.Number().Nullable().Readonly()},
	}
}

func soundSpecs() []ui.Group {
	return []ui.Group{{ID: "identity", Title: "Identity", Columns: 2}, {ID: "profile", Title: "Profile"}}
}

// TestLintAcceptsSoundDeclaration is the baseline: the lint is silent on a
// clean declaration. Without it any test below could be green for the wrong
// reason.
func TestLintAcceptsSoundDeclaration(t *testing.T) {
	if got := Lint(sound(), soundSpecs()); len(got) != 0 {
		t.Errorf("Lint on a sound declaration = %v, want none", got)
	}
}

// TestLintWithoutSpecs: an editor without sections is legitimate (a small
// form), so the section checks are skipped while the write checks are not.
func TestLintWithoutSpecs(t *testing.T) {
	if got := Lint(sound(), nil); len(got) != 0 {
		t.Errorf("Lint without specs = %v, want none (section checks must be skipped)", got)
	}
	broken := Set[wcell]{{Name: "a", Col: ro(), UI: ui.String()}}
	if len(Lint(broken, nil)) == 0 {
		t.Error("Lint without specs missed an unwritable field that is not read-only")
	}
}

// TestLintCatchesEachDefect: one broken declaration per case and the substring
// the lint must utter. What is checked is that it FIRES: a lint that catches
// nothing is indistinguishable from none.
func TestLintCatchesEachDefect(t *testing.T) {
	cases := map[string]struct {
		set   Set[wcell]
		specs []ui.Group
		want  string
	}{
		"empty declaration": {
			set: nil, specs: soundSpecs(), want: "declaration is empty",
		},
		"duplicate name": {
			set: Set[wcell]{
				{Name: "a", Group: "identity", Col: rw(), UI: ui.String()},
				{Name: "a", Group: "identity", Col: rw(), UI: ui.String()},
			},
			specs: []ui.Group{{ID: "identity", Title: "I"}},
			want:  "declared twice",
		},
		// A column without an assign that is not marked read-only: the engine
		// does not strip it, the form draws an input, and the edit silently
		// vanishes.
		"unwritable but editable": {
			set:   Set[wcell]{{Name: "rating", Group: "profile", Col: ro(), UI: ui.Number()}},
			specs: []ui.Group{{ID: "profile", Title: "P"}},
			want:  "not marked read-only",
		},
		// The other side of the same fact: writes allowed, the form forbids.
		"writable but read-only": {
			set:   Set[wcell]{{Name: "city", Group: "profile", Col: rw(), UI: ui.String().Readonly()}},
			specs: []ui.Group{{ID: "profile", Title: "P"}},
			want:  "edits would silently vanish",
		},
		"required but unwritable": {
			set:   Set[wcell]{{Name: "x", Group: "profile", Col: ro(), UI: ui.String().Required().Readonly()}},
			specs: []ui.Group{{ID: "profile", Title: "P"}},
			want:  "create could never fill it",
		},
		"required and read-only": {
			set:   Set[wcell]{{Name: "x", Group: "profile", Col: ro(), UI: ui.String().Required().Readonly()}},
			specs: []ui.Group{{ID: "profile", Title: "P"}},
			want:  "missing property",
		},
		"undeclared group": {
			set:   Set[wcell]{{Name: "a", Group: "typo", Col: rw(), UI: ui.String()}},
			specs: []ui.Group{{ID: "identity", Title: "I"}},
			want:  "not in the section registry",
		},
		"visible field without a section": {
			set:   Set[wcell]{{Name: "a", Group: "identity", Col: rw(), UI: ui.String()}, {Name: "orphan", Col: rw(), UI: ui.String()}},
			specs: []ui.Group{{ID: "identity", Title: "I"}},
			want:  "belongs to no section",
		},
		"hidden field inside a section": {
			set:   Set[wcell]{{Name: "id", Group: "identity", Col: ro(), UI: ui.String().Hidden().Readonly()}},
			specs: []ui.Group{{ID: "identity", Title: "I"}},
			want:  "the form never renders",
		},
		"unused section": {
			set:   Set[wcell]{{Name: "a", Group: "identity", Col: rw(), UI: ui.String()}},
			specs: []ui.Group{{ID: "identity", Title: "I"}, {ID: "ghost", Title: "G"}},
			want:  "no field references it",
		},
		"section without a title": {
			set:   Set[wcell]{{Name: "a", Group: "identity", Col: rw(), UI: ui.String()}},
			specs: []ui.Group{{ID: "identity"}},
			want:  "empty title",
		},
		"columns out of the TS union": {
			set:   Set[wcell]{{Name: "a", Group: "identity", Col: rw(), UI: ui.String()}},
			specs: []ui.Group{{ID: "identity", Title: "I", Columns: 3}},
			want:  "want 1 or 2",
		},
		// A typo in ParentField: the options request goes out with an empty
		// parent, the select is always empty, and there is neither an error
		// nor a warning.
		"parentField names no field": {
			set: Set[wcell]{
				{Name: "trip_id", Group: "identity", Col: rw(), UI: ui.String()},
				{Name: "trip_entry_id", Group: "identity", Col: rw(),
					UI: ui.Relation("trip_entries", false).ParentField("tripid")},
			},
			specs: []ui.Group{{ID: "identity", Title: "I"}},
			want:  "parentField names",
		},
		// Delegation to ui.Lint: JSON Schema silently ignores maxLength on a
		// boolean property, so the constraint is lost without a signal.
		"keyword does not fit the type": {
			set:   Set[wcell]{{Name: "flag", Group: "identity", Col: rw(), UI: ui.Bool().MaxLen(50)}},
			specs: []ui.Group{{ID: "identity", Title: "I"}},
			want:  "maxLength",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := Lint(tc.set, tc.specs)
			for _, p := range got {
				if strings.Contains(p, tc.want) {
					return
				}
			}
			t.Errorf("Lint did not report %q; got %v", tc.want, got)
		})
	}
}

// TestLintAcceptsParentFieldNamingADeclaredField is the other half of the same
// gate: a name that IS in the declaration must pass silently, or every real
// relation with a parent field would go red.
func TestLintAcceptsParentFieldNamingADeclaredField(t *testing.T) {
	set := Set[wcell]{
		{Name: "trip_id", Group: "identity", Col: rw(), UI: ui.String()},
		{Name: "trip_entry_id", Group: "identity", Col: rw(),
			UI: ui.Relation("trip_entries", false).ParentField("trip_id")},
	}
	if got := Lint(set, []ui.Group{{ID: "identity", Title: "I"}}); len(got) != 0 {
		t.Errorf("Lint = %v, want none", got)
	}
}

// TestLintOrderIsDeterministic: the output ends up in a failing test's message
// and must not flicker between runs; the declaration is walked in declared
// order, the section registry in its own.
func TestLintOrderIsDeterministic(t *testing.T) {
	broken := Set[wcell]{
		{Name: "zeta", Group: "identity", Col: ro(), UI: ui.String()},
		{Name: "alpha", Group: "identity", Col: ro(), UI: ui.String()},
	}
	specs := []ui.Group{{ID: "identity", Title: "I"}}

	first := Lint(broken, specs)
	if len(first) < 2 {
		t.Fatalf("expected both fields reported, got %v", first)
	}
	if !strings.HasPrefix(first[0], `"zeta"`) {
		t.Errorf("problems[0] = %q, want the first declared field", first[0])
	}
	for i := 0; i < 5; i++ {
		again := Lint(broken, specs)
		for j := range first {
			if again[j] != first[j] {
				t.Fatalf("run %d differs at %d:\n%q\n%q", i, j, again[j], first[j])
			}
		}
	}
}

// TestLintChecksTheTypeAxis pins the type axis: without it ui.List(...) next
// to a string cell lints clean and silently never writes, the form sends a
// shape the cell does not parse, assign returns ok=false, the column is
// skipped and Save "succeeds" without the field. No error, no journal row.
func TestLintChecksTheTypeAxis(t *testing.T) {
	cases := []struct {
		name string
		cell fakeCell
		ui   ui.Field
		want bool // a violation is expected
	}{
		{"list on a string cell", cellOf(ui.Wire{Kind: ui.KindString}), ui.List(ui.String()), true},
		{"string on a list cell", cellOf(ui.Wire{Kind: ui.KindList, Elem: ui.KindString}), ui.String(), true},
		{"integer list on a string list cell", cellOf(ui.Wire{Kind: ui.KindList, Elem: ui.KindString}), ui.List(ui.Integer()), true},
		{"items on a list cell", cellOf(ui.Wire{Kind: ui.KindList, Elem: ui.KindString}), ui.Items(), true},
		{"matching string", cellOf(ui.Wire{Kind: ui.KindString}), ui.String(), false},
		{"matching list", cellOf(ui.Wire{Kind: ui.KindList, Elem: ui.KindString}), ui.List(ui.String()), false},
		{"any accepts everything", cellOf(ui.Wire{Kind: ui.KindAny}), ui.List(ui.String()), false},
		// The `if writable` guard as a test: without it this declaration would
		// go falsely red although the cell never writes. Accepts=false is
		// deliberate here (see readonlyMismatch); real read-only cells cannot
		// do that.
		{"read-only cell whose Accepts would reject — guard must still skip it", readonlyMismatch{}, ui.List(ui.String()), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Lint(Set[fakeCell]{{Name: "x", Col: c.cell, UI: c.ui}}, nil)
			hasTypeProblem := false
			for _, p := range got {
				if strings.Contains(p, "the storage cell does not accept") {
					hasTypeProblem = true
				}
			}
			if hasTypeProblem != c.want {
				t.Errorf("Lint() = %v, want type problem = %v", got, c.want)
			}
		})
	}
}

// TestLintRecursesIntoItems: violations inside an item are the same class of
// defect as at the top and must not hide behind the nesting boundary.
func TestLintRecursesIntoItems(t *testing.T) {
	inner := Set[fakeCell]{
		{Name: "day", Col: readonlyCell{}, UI: ui.Integer()}, // cell does not write, field not read-only
	}
	got := Lint(Set[fakeCell]{
		{Name: "itinerary", Col: cellOf(ui.Wire{Kind: ui.KindItems}), Items: inner, UI: ui.Items()},
	}, nil)
	found := false
	for _, p := range got {
		if strings.Contains(p, "itinerary.day") {
			found = true
		}
	}
	if !found {
		t.Errorf("Lint() = %v, want a problem inside the item, prefixed with itinerary.day", got)
	}
}

// TestLintRecursesTypeAxisIntoItems: TestLintRecursesIntoItems above proves
// the read-only axis sees violations inside an element; this proves the TYPE
// axis does too - a defect one level deeper is not a different class of
// defect, and it must not become invisible just because it moved inside Items.
func TestLintRecursesTypeAxisIntoItems(t *testing.T) {
	inner := Set[fakeCell]{
		{Name: "day", Col: cellOf(ui.Wire{Kind: ui.KindString}), UI: ui.List(ui.String())},
	}
	got := Lint(Set[fakeCell]{
		{Name: "itinerary", Col: cellOf(ui.Wire{Kind: ui.KindItems}), Items: inner, UI: ui.Items()},
	}, nil)
	found := false
	for _, p := range got {
		if strings.Contains(p, "itinerary.day") && strings.Contains(p, "the storage cell does not accept") {
			found = true
		}
	}
	if !found {
		t.Errorf("Lint() = %v, want a type-axis problem inside the item, prefixed with itinerary.day", got)
	}
}

// TestLintDoesNotDuplicateKeywordFindingsInsideItems: ui.Lint (via s.Fields())
// recurses into Items on its own (ui/lint.go) and reports the keyword
// violation WITH the full path ("itinerary.flag"). A second ui.Lint on the
// bare inner Set inside lintNested would report the same finding again,
// unprefixed, and with two items sharing a subfield name the bare copies
// would be indistinguishable. Counting, not just Contains, is the only way to
// pin that.
func TestLintDoesNotDuplicateKeywordFindingsInsideItems(t *testing.T) {
	inner := Set[fakeCell]{
		// maxLength does not apply to boolean, the same bait as
		// TestLintCatchesEachDefect["keyword does not fit the type"], only
		// inside an item.
		{Name: "flag", Col: cellOf(ui.Wire{Kind: ui.KindBool}), UI: ui.Bool().MaxLen(50)},
	}
	got := Lint(Set[fakeCell]{
		{Name: "itinerary", Col: cellOf(ui.Wire{Kind: ui.KindItems}), Items: inner, UI: ui.Items()},
	}, nil)

	count, pathed := 0, false
	for _, p := range got {
		if strings.Contains(p, "maxLength") {
			count++
			if strings.Contains(p, "itinerary.flag") {
				pathed = true
			}
		}
	}
	if count != 1 {
		t.Errorf("Lint() reported %d maxLength findings, want exactly 1 (no duplicate): %v", count, got)
	}
	if !pathed {
		t.Errorf("the single maxLength finding must be prefixed with itinerary.flag: %v", got)
	}
}

// TestLintAcceptsKeyedItemsPair: the keyed-items pair is declarable, both
// gates (Entry.Items vs UI and the type axis) are green on an honest pair.
func TestLintAcceptsKeyedItemsPair(t *testing.T) {
	inner := Set[fakeCell]{
		{Name: "day", Col: wireCell{ui.Wire{Kind: ui.KindInteger}}, UI: ui.Integer()},
	}
	s := Set[fakeCell]{
		{Name: "blocks", Col: wireCell{ui.Wire{Kind: ui.KindMap, Elem: ui.KindItems}},
			Items: inner, UI: ui.Keyed(ui.Items(), ui.Keys("en", "ru"))},
	}
	if problems := Lint(s, nil); len(problems) != 0 {
		t.Errorf("clean keyed-items declaration lints dirty: %v", problems)
	}
}

// TestLintRejectsEntryItemsOverPlainKeyed: Entry.Items next to Keyed(List) is
// a finding about the UI half, like Entry.Items next to ui.List.
func TestLintRejectsEntryItemsOverPlainKeyed(t *testing.T) {
	inner := Set[fakeCell]{{Name: "day", Col: writable{}, UI: ui.Integer()}}
	s := Set[fakeCell]{
		{Name: "blocks", Col: writable{}, Items: inner,
			UI: ui.Keyed(ui.List(ui.String()), ui.Keys("en"))},
	}
	found := false
	for _, p := range Lint(s, nil) {
		if strings.Contains(p, "Entry.Items") {
			found = true
		}
	}
	if !found {
		t.Error("Entry.Items over Keyed(List) lints clean — the gate must name the UI half")
	}
}

// TestLintCatchesItemsUIMismatch: Entry.Items declared but the UI half is not
// ui.Items() - Fields overwrites the field with an object-array schema
// regardless of what the UI constructor said (decl.go keys off
// len(f.Items) > 0, not f.UI.IsItems()), so the type axis above (which reads
// the ORIGINAL, not-yet-overwritten WireKind) sees no mismatch and passes
// silently. The reverse mistake - ui.Items() without Entry.Items - IS already
// caught by ui.Lint ("did you forget decl.Entry.Items?"); this closes the
// other direction.
func TestLintCatchesItemsUIMismatch(t *testing.T) {
	inner := Set[fakeCell]{
		{Name: "day", Col: cellOf(ui.Wire{Kind: ui.KindInteger}), UI: ui.Integer()},
	}
	got := Lint(Set[fakeCell]{
		{Name: "itinerary", Col: cellOf(ui.Wire{Kind: ui.KindList, Elem: ui.KindString}), Items: inner, UI: ui.List(ui.String())},
	}, nil)
	found := false
	for _, p := range got {
		if strings.Contains(p, `"itinerary"`) && strings.Contains(p, "ui.Items()") {
			found = true
		}
	}
	if !found {
		t.Errorf("Lint() = %v, want a finding about Entry.Items without ui.Items()", got)
	}
}
