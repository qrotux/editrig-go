// Package decl is the author-facing surface of an editable entity: an ordered
// set of "form field <-> storage cell" records from which everything else is
// derived: render order, section membership, the short schema, SELECT
// projections and SET assignments.
//
// THE PACKAGE KNOWS NO STORAGE. The cell arrives as a type parameter: Set[C]
// never calls a single method on C, so a Postgres cell (erjet.Column), a cell
// of another driver, or anything else fits equally. There is no driver import
// here and cannot be one, as in the core.
//
// C is deliberately NOT constrained by an interface: a constraint would declare
// a capability the package does not use. Gates that need a cell's writability
// get by with a free function taking its own, narrower parameter; the type
// does not change for that.
//
// Why a separate package rather than a file in the core or in ui:
//
//   - the core is the PROTOCOL (Entity, HTTP, validation): it does not know
//     what an entity is made of, and its dependency contract (doc.go) excludes
//     ui and decl.
//   - ui is the presentation VOCABULARY, a leaf with no dependencies.
//   - decl is what the entity author writes, and the only place where
//     presentation and persistence stand side by side. A third role, with its
//     own home.
package decl

import (
	"log/slog"

	"github.com/qrotux/editrig-go/ui"
)

// Entry is ONE declaration record: name, storage cell and form field.
//
// Cell and field stand side by side and the slice itself gives the order: no
// order list and no separate column and field maps exist, so the field name
// is written once and nothing can drift.
type Entry[C any] struct {
	Name string
	// Group is the section id from the entity's registry; empty renders the
	// field outside any section. The RECORD holds the reference, not a list
	// inside the section: the field name is not written twice, and "a section
	// references a missing field" becomes inexpressible.
	Group string
	// Col says how the field is read and written. What a cell means only its
	// package knows (for Postgres, erjet.Column, where a missing assign makes
	// a write physically unreachable).
	Col C
	// UI says how the field looks: both halves of the rjsf document at once.
	// The title is NOT given here, it derives from Name (see Fields);
	// requiredness is, through the ui.Field.Required modifier.
	UI ui.Field
	// Items is the ITEM declaration when the field is a repeatable block (a
	// list of objects); empty means a plain field.
	//
	// The same type as the entity itself: a child-table item is a set of
	// "field <-> cell" records, and a second language for it would mean a
	// second cell vocabulary, a second lint and a second way to err. The
	// recursion through the slice is legal (Set[C] is []Entry[C]).
	//
	// The presentation counterpart is ui.Items() in UI: the constructor
	// declares the field a list, and Fields fills in the item's composition.
	// The storage counterpart is erjet.ChildRows, which receives this same set
	// as its second argument.
	Items Set[C]
}

// Set is the ordered record set of an entity. Order is significant: ui:order,
// the order of SELECT projections and the order of SET assignments all come
// from it.
type Set[C any] []Entry[C]

// Fields is the presentation projection: the field map with titles and enum
// labels filled in by name.
//
// title/label are the entity's catalog functions: (field name) -> title and
// (field name, value) -> value label. The whole link to the i18n catalog lives
// in them alone, which is why a record does not repeat its own name.
func (s Set[C]) Fields(title func(name string) string, label func(name, value string) string) map[string]ui.Field {
	out := make(map[string]ui.Field, len(s))
	for _, f := range s {
		v := f.UI.Title(title(f.Name))
		name := f.Name
		valueLabel := func(value string) string { return label(name, value) }
		// VALUE labels come from the catalog the same way for both kinds: an
		// enum's variants and a keyed field's keys. Both are declared as bare
		// values because labels are localized and the declaration is static.
		switch {
		// A list of objects: the item's composition comes from the nested
		// declaration, its subfield labels from the same catalog by the same
		// walk. The subfield key is "<field>_fields.<sub>": dotted nesting
		// ("editor.<entity>.<field>.<sub>") is impossible because the message
		// catalog is nested JSON and a field name cannot be both a title
		// string and a parent object, the same reason value labels live under
		// the "_values" suffix (catalog.go in the core).
		case len(f.Items) > 0:
			sub := f.Items
			subFields := sub.Fields(
				func(n string) string { return title(name + ui.SubfieldSuffix + n) },
				func(n, value string) string { return label(name+ui.SubfieldSuffix+n, value) },
			)
			if v.IsKeyedItems() {
				// The switch branches are exclusive: without KeyLabels here a
				// keyed-items field would never get them (the v.IsKeyed() case
				// below is unreachable) and the switcher would show raw
				// "en"/"ru".
				v = v.WithKeyedItems(subFields, sub.Order()).KeyLabels(valueLabel)
			} else {
				v = v.WithItems(subFields, sub.Order())
			}
		case v["enum"] != nil:
			v = v.EnumLabels(valueLabel)
		case v.IsKeyed():
			v = v.KeyLabels(valueLabel)
		}
		out[f.Name] = v
	}
	return out
}

// Order returns the field names in declaration order; ui:order derives from
// it, so no separate order list exists.
func (s Set[C]) Order() []string {
	out := make([]string, len(s))
	for i, f := range s {
		out[i] = f.Name
	}
	return out
}

// Groups fills the membership of the declared sections from the Group
// references on the records themselves.
//
// SECTION order comes from the specs registry, not from the position of the
// first field: the layout can be rearranged without moving records (the same
// slice orders the SELECT projections). FIELD order within a section follows
// the declaration.
//
// A field referencing an undeclared section loses the reference and renders
// outside any section: it is a typo in code, and failing the whole schema for
// it is worse than showing the field in the wrong frame. The warning is a
// runtime safety net; the real guard is the entity's gate over
// UndeclaredGroups, which keeps such a typo out of production.
//
// A section without a single member is omitted: the client does not draw an
// empty frame anyway, and in the document it would be noise.
func (s Set[C]) Groups(specs []ui.Group) []ui.Group {
	known := make(map[string]bool, len(specs))
	for _, g := range specs {
		known[g.ID] = true
	}

	members := make(map[string][]string, len(specs))
	for _, f := range s {
		switch {
		case f.Group == "":
			continue
		case !known[f.Group]:
			slog.Warn("editor: field references an undeclared group, rendering it outside any section",
				"field", f.Name, "group", f.Group)
			continue
		}
		members[f.Group] = append(members[f.Group], f.Name)
	}

	out := make([]ui.Group, 0, len(specs))
	for _, g := range specs {
		if len(members[g.ID]) == 0 {
			continue
		}
		g.Fields = members[g.ID]
		out = append(out, g)
	}
	return out
}

// UndeclaredGroups returns the section names fields reference but specs does
// not declare, with the names of the referencing fields; for the entity gate.
func (s Set[C]) UndeclaredGroups(specs []ui.Group) map[string][]string {
	known := make(map[string]bool, len(specs))
	for _, g := range specs {
		known[g.ID] = true
	}
	out := map[string][]string{}
	for _, f := range s {
		if f.Group != "" && !known[f.Group] {
			out[f.Group] = append(out[f.Group], f.Name)
		}
	}
	return out
}
