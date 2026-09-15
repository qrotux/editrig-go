package decl

import (
	"fmt"

	"github.com/qrotux/editrig-go/ui"
)

// Cell is a storage cell that can be asked whether it accepts writes and
// whether it can parse the declared wire shape.
//
// The constraint is declared HERE, on Lint, not on Set: the declaration itself
// asks the cell nothing, and demanding a capability it does not use would lie
// about its dependencies. A free function with its own, narrower parameter
// gives the gate all it needs without changing the type.
type Cell interface {
	// Writable reports whether the cell accepts writes. For Postgres that is
	// the presence of an assign (erjet.Column); its absence makes a write
	// physically unreachable.
	Writable() bool
	// Accepts reports whether the cell can parse a value of the declared
	// shape. The gate's second question after writability, orthogonal to it:
	// the read-only axis checks whether one MAY write, this one whether what
	// the form sends CAN BE PARSED.
	Accepts(w ui.Wire) bool
}

// Lint checks the agreements WITHIN a declaration and returns the violations
// (empty means clean). Run from the entity's test, not at runtime: every
// violation is a declaration defect, permanently present in the code or
// permanently absent.
//
// An engine gate rather than tests per entity: every check below is an editor
// invariant, not domain logic, and each entity would otherwise copy the same
// tests and then fall behind the next check added.
//
// Violation order is deterministic (the declaration walk, then the section
// registry): the list ends up in test output and must not flicker between
// runs.
//
// specs is the entity's section registry. Empty skips the section checks: an
// editor without sections is a legitimate (small) form, not a defect.
func Lint[C Cell](s Set[C], specs []ui.Group) []string {
	problems := lint("", s, specs)

	// JSON Schema keywords against field types are checked EXACTLY ONCE, here
	// at the top level, not inside lint (where lintNested would repeat it at
	// every nesting level). ui.Lint already recurses into Items on its own
	// (ui/lint.go) and builds the full path from s.Fields(), which contains
	// the nested subfields because Set.Fields fills them in through WithItems.
	// Called again on a bare f.Items inside lintNested, the same finding would
	// sound twice: once with the path ("itinerary.day"), once without.
	//
	// The catalog functions are identities: the lint looks at KEYS and types,
	// not at the copy, and labels do not affect its output.
	problems = append(problems, ui.Lint(s.Fields(
		func(name string) string { return name },
		func(_, value string) string { return value },
	))...)

	return problems
}

// lint is the shared body of the top-level call and the recursion into
// Entry.Items (lintNested). prefix is the path to the current nesting level,
// "" at the top and "itinerary" inside an item; it prefixes the field name in
// every message (see named).
func lint[C Cell](prefix string, s Set[C], specs []ui.Group) []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if len(s) == 0 {
		return []string{"declaration is empty — the editor would render a form with no fields"}
	}

	known := make(map[string]bool, len(specs))
	for _, g := range specs {
		known[g.ID] = true
	}
	used := make(map[string]bool, len(specs))

	// The whole declaration is needed BEFORE the walk: parentField may
	// legitimately reference a field declared below it.
	declared := make(map[string]bool, len(s))
	for _, f := range s {
		declared[f.Name] = true
	}

	seen := make(map[string]bool, len(s))
	for _, f := range s {
		if seen[f.Name] {
			add("%q is declared twice — the later record silently wins in every map built from the declaration", named(prefix, f.Name))
		}
		seen[f.Name] = true

		writable, readonly := f.Col.Writable(), f.UI.IsReadonly()

		// Persistence and presentation must agree in both directions: the main
		// gate of the read-only contract, and the reason no third, intermediate
		// list of read-only fields exists.
		switch {
		case !writable && !readonly:
			add("%q: the storage cell takes no writes but the field is not marked read-only — "+
				"the engine will not strip it, and the form renders an editable input", named(prefix, f.Name))
		case writable && readonly:
			add("%q: the storage cell accepts writes but the field is marked read-only — "+
				"the engine strips it before Save, so edits would silently vanish", named(prefix, f.Name))
		}

		if f.UI.IsRequired() {
			if !writable {
				add("%q: required, but the storage cell takes no writes — create could never fill it", named(prefix, f.Name))
			}
			if readonly {
				add("%q: required and read-only — the engine strips read-only keys BEFORE structural "+
					"validation, so every Submit would fail with \"missing property\"", named(prefix, f.Name))
			}
		}

		// The type axis, checked ONLY on writable fields: a read-only cell has
		// no parse path at all and declares KindAny, so a red gate here would
		// be false.
		if writable {
			if w := f.UI.WireKind(); !f.Col.Accepts(w) {
				add("%q: the storage cell does not accept %s values — the form would send a shape the cell "+
					"cannot parse, assign would return ok=false, the column would be skipped, and Save would "+
					"\"succeed\" without writing the field", named(prefix, f.Name), describe(w))
			}
		}

		// parentField is read by the FIELD NAME of this same declaration: the
		// form sends its value in the options request. A name absent from the
		// declaration gives neither an error nor a warning, the select is just
		// always empty.
		//
		// The check is per level, each against its own declared: a nested
		// parentField referencing a ROOT field would be a false finding (the
		// root's composition is not reachable from here).
		if parent, ok := f.UI.ParentFieldName(); ok && !declared[parent] {
			add("%q: ui:options.parentField names %q, which is no field of this declaration — "+
				"the form would send an empty parent and the options list would stay silently empty",
				named(prefix, f.Name), parent)
		}

		// A list of objects: the item is a declaration like any other, with the
		// same invariants.
		if len(f.Items) > 0 {
			// Entry.Items and UI must agree like writable and readonly above:
			// Set.Fields keys off len(f.Items) > 0 (decl.go), not
			// f.UI.IsItems(), and WithItems overwrites "items" with an object
			// schema REGARDLESS of what the UI constructor declared. Without
			// this check Entry.Items next to ui.List(...)/ui.String() lints
			// clean: the type axis above compares the DECLARED (not yet
			// overwritten) WireKind, which trivially matches its own cell. The
			// outcome is what the type-axis message describes, the form sends
			// a shape the cell does not parse, just reached another way. The
			// reverse mistake (ui.Items() without Entry.Items) is caught by
			// ui.Lint ("did you forget decl.Entry.Items?").
			//
			// The second admissible half is ui.Keyed(ui.Items(), ...): the same
			// Entry.Items + item cards pair, under locale/variant keys (WireKind
			// {KindMap, KindItems}). Entry.Items next to ANY other UI half,
			// Keyed(List(...)) included, is rejected with the same message.
			if !f.UI.IsItems() && !f.UI.IsKeyedItems() {
				add("%q: declares Entry.Items but the UI half is neither ui.Items() nor ui.Keyed(ui.Items(), …) — "+
					"Fields overwrites it with an object-array schema anyway, so the form renders item cards "+
					"while the storage cell still parses %s", named(prefix, f.Name), describe(f.UI.WireKind()))
			}
			problems = append(problems, lintNested(named(prefix, f.Name), f.Items)...)
		}

		if len(specs) == 0 {
			continue
		}
		switch {
		case f.Group == "":
			// Hidden fields belong to no section: the header (ui.Header) lifts
			// their values, and the form body never draws them.
			if !f.UI.IsHidden() {
				add("%q is visible but belongs to no section — it would fall into the unnamed tail section", named(prefix, f.Name))
			}
		case !known[f.Group]:
			add("%q references group %q, which is not in the section registry — "+
				"the field would render outside any section, with a warning on every request", named(prefix, f.Name), f.Group)
		case f.UI.IsHidden():
			add("%q is hidden but assigned to section %q — the section would list a field the form never renders",
				named(prefix, f.Name), f.Group)
		default:
			used[f.Group] = true
		}
	}

	for _, g := range specs {
		if !used[g.ID] {
			add("group %q is declared but no field references it — its chrome and its i18n keys are dead", g.ID)
		}
		if g.Title == "" {
			add("group %q: empty title — the section would render an unlabeled frame "+
				"(most likely a missing i18n key)", g.ID)
		}
		// The client types columns as the union `1 | 2`; Go's type system has
		// no such union, so this gate guards the range. Zero is the legitimate
		// "key omitted, the client takes its default" (ui.Group.Columns is
		// omitempty).
		if g.Columns < 0 || g.Columns > 2 {
			add("group %q: columns = %d, want 1 or 2 (or zero to omit the key)", g.ID, g.Columns)
		}
	}

	return problems
}

// lintNested lints the item of a repeatable block: the same walk, without a
// section registry and WITHOUT a second ui.Lint (run exactly once, in Lint,
// which already covers the nested set through the top-level s.Fields()).
//
// Sections do not descend into an item: it is drawn as an array card, not as
// a form with frames.
func lintNested[C Cell](prefix string, s Set[C]) []string {
	return lint(prefix, s, nil)
}

// named is the field name with its nesting path ("itinerary.day" instead of a
// bare "day"); without it a finding inside an item cannot be located.
func named(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// describe spells out a wire shape for a lint message.
func describe(w ui.Wire) string {
	if w.Elem == "" || w.Elem == ui.KindAny {
		return string(w.Kind)
	}
	return string(w.Kind) + " of " + string(w.Elem)
}
