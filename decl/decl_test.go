package decl

import (
	"reflect"
	"testing"

	"github.com/qrotux/editrig-go/ui"
)

// cell is the test's storage cell, deliberately meaningless: the package calls
// nothing on C, and this proves it, a struct without a single method fits the
// type parameter.
type cell struct{ col string }

func sample() Set[cell] {
	return Set[cell]{
		{Name: "id", Col: cell{"id"}, UI: ui.String().Hidden().Readonly()},
		{Name: "username", Group: "identity", Col: cell{"username"},
			UI: ui.String().Required().MaxLen(50)},
		{Name: "name", Group: "identity", Col: cell{"name"}, UI: ui.String().Required()},
		{Name: "role", Group: "profile", Col: cell{"role"}, UI: ui.Enum("admin", "guest")},
		{Name: "city", Group: "profile", Col: cell{"city"}, UI: ui.String().Nullable()},
	}
}

// title/label are the test's catalog functions: labels derive from the NAME,
// so a record does not repeat its own name.
func title(name string) string        { return "T:" + name }
func label(name, value string) string { return "L:" + name + ":" + value }

// fakeCell is the alias of decl.Cell for this package's test declarations:
// different concrete doubles (writable here, cellOf/readonlyCell in
// lint_test.go) declare different writability and wire shapes while staying
// one Set type parameter. An interface, not a struct, or readonlyCell would
// not be assignable to Col next to writable/cellOf and every combination
// would need its own Set type.
type fakeCell = Cell

// writable is a cell that always writes and accepts any wire shape. Fit where
// only writability matters (TestFieldsFillsItemsFromNestedDeclaration below:
// Fields asks Col nothing), not the type axis itself, which cellOf and
// readonlyCell in lint_test.go answer.
type writable struct{}

func (writable) Writable() bool       { return true }
func (writable) Accepts(ui.Wire) bool { return true }

// TestFieldsFillsItemsFromNestedDeclaration: the item declaration is written
// ONCE (Entry.Items) and feeds both halves, the storage cell and the ui.Items
// composition. No second list of item fields exists, exactly as at the top
// level.
func TestFieldsFillsItemsFromNestedDeclaration(t *testing.T) {
	inner := Set[fakeCell]{
		{Name: "day", Col: writable{}, UI: ui.Integer().Required()},
		{Name: "kind", Col: writable{}, UI: ui.Enum("hike", "boat")},
	}
	set := Set[fakeCell]{
		{Name: "itinerary", Col: writable{}, Items: inner, UI: ui.Items()},
	}

	fields := set.Fields(
		func(name string) string { return "T:" + name },
		func(name, value string) string { return "L:" + name + ":" + value },
	)

	props, entries := ui.Split(fields)
	item := props["itinerary"].(map[string]any)["items"].(map[string]any)
	day := item["properties"].(map[string]any)["day"].(map[string]any)
	if day["title"] != "T:itinerary_fields.day" {
		t.Errorf("subfield title = %v, want T:itinerary_fields.day", day["title"])
	}

	entry := entries["itinerary"].(map[string]any)["items"].(map[string]any)
	kindUI := entry["kind"].(map[string]any)
	names, _ := kindUI["ui:enumNames"].([]string)
	if len(names) != 2 || names[0] != "L:itinerary_fields.kind:hike" {
		t.Errorf("enum labels inside the item = %v", names)
	}
	if props["itinerary"].(map[string]any)["title"] != "T:itinerary" {
		t.Error("the list itself lost its own title")
	}
}

// TestFieldsComposesKeyedItems: the len(Items)>0 branch must make BOTH calls
// for keyed items, WithKeyedItems and KeyLabels; the switch branches are
// exclusive, and without the explicit call the key labels would be lost
// silently.
func TestFieldsComposesKeyedItems(t *testing.T) {
	s := Set[fakeCell]{
		{Name: "blocks", Col: writable{}, Items: Set[fakeCell]{
			{Name: "day", Col: writable{}, UI: ui.Integer()},
		}, UI: ui.Keyed(ui.Items(), ui.Keys("en", "ru"))},
	}
	f := s.Fields(title, label)["blocks"]

	en := f["properties"].(map[string]any)["en"].(map[string]any)["items"].(map[string]any)
	day := en["properties"].(map[string]any)["day"].(map[string]any)
	if day["title"] != "T:blocks"+ui.SubfieldSuffix+"day" {
		t.Errorf("subfield title = %v, want the catalog key with SubfieldSuffix", day["title"])
	}
	keys := f["ui:options"].(map[string]any)["keys"].([]ui.Key)
	if len(keys) == 0 || keys[0].Label != "L:blocks:en" {
		t.Errorf("keys = %+v — KeyLabels was not applied in the keyed-items branch", keys)
	}
}

func TestOrderFollowsDeclaration(t *testing.T) {
	want := []string{"id", "username", "name", "role", "city"}
	if got := sample().Order(); !reflect.DeepEqual(got, want) {
		t.Errorf("Order() = %v, want %v", got, want)
	}
}

// TestFieldsDeriveTitlesAndEnumLabels: title and enum labels are filled in
// from the field name by the catalog functions; enum labels are positional to
// their "enum".
func TestFieldsDeriveTitlesAndEnumLabels(t *testing.T) {
	fields := sample().Fields(title, label)

	if fields["city"]["title"] != "T:city" {
		t.Errorf("city title = %v, want T:city", fields["city"]["title"])
	}
	names, ok := fields["role"]["ui:enumNames"].([]string)
	if !ok {
		t.Fatalf("role has no ui:enumNames: %#v", fields["role"])
	}
	if !reflect.DeepEqual(names, []string{"L:role:admin", "L:role:guest"}) {
		t.Errorf("role enumNames = %v", names)
	}
	// A non-enum field gets no value labels.
	if _, has := fields["city"]["ui:enumNames"]; has {
		t.Error("city (not an enum) got ui:enumNames")
	}
}

// TestFieldsDoNotMutateDeclaration: Fields runs on EVERY request (titles
// depend on the locale). Were it to write into ui.Field in place, the first
// request would stamp its locale into the shared declaration for all that
// follow.
func TestFieldsDoNotMutateDeclaration(t *testing.T) {
	s := sample()
	_ = s.Fields(title, label)
	if _, leaked := s[4].UI["title"]; leaked {
		t.Errorf("Fields wrote a title into the declaration: %#v", s[4].UI)
	}
	other := s.Fields(func(n string) string { return "RU:" + n }, label)
	if other["city"]["title"] != "RU:city" {
		t.Errorf("second call got a stale title: %v", other["city"]["title"])
	}
}

// TestGroupsTakeOrderFromSpecs: the registry orders sections, not the position
// of the first field; the layout is rearranged without moving records (the
// same slice orders the SELECT projections).
func TestGroupsTakeOrderFromSpecs(t *testing.T) {
	// profile is declared FIRST although its fields come after identity's in
	// the declaration.
	got := sample().Groups([]ui.Group{
		{ID: "profile", Title: "P"},
		{ID: "identity", Title: "I"},
	})
	if len(got) != 2 {
		t.Fatalf("Groups() = %+v, want two sections", got)
	}
	if got[0].ID != "profile" || got[1].ID != "identity" {
		t.Errorf("section order = %v,%v — want specs order profile,identity", got[0].ID, got[1].ID)
	}
	// Within a section, declaration order.
	if !reflect.DeepEqual(got[1].Fields, []string{"username", "name"}) {
		t.Errorf("identity fields = %v, want declaration order", got[1].Fields)
	}
	// The spec comes back with its membership filled in but keeps its chrome:
	// the registry Title is not lost.
	if got[0].Title != "P" {
		t.Errorf("section lost its title: %+v", got[0])
	}
}

// TestGroupsDropEmptySections: a section without a single member is omitted
// from the document; the client draws no empty frame, and in a golden file it
// would be noise.
func TestGroupsDropEmptySections(t *testing.T) {
	got := sample().Groups([]ui.Group{{ID: "identity"}, {ID: "nobody"}})
	if len(got) != 1 || got[0].ID != "identity" {
		t.Errorf("Groups() = %+v, want only identity", got)
	}
}

// TestUndeclaredGroupDegradesNotCrashes: a field referencing a missing section
// renders OUTSIDE any section instead of failing the document, and
// UndeclaredGroups hands it to the gate.
func TestUndeclaredGroupDegradesNotCrashes(t *testing.T) {
	s := append(sample(), Entry[cell]{Name: "orphan", Group: "typo", Col: cell{"orphan"}, UI: ui.String()})
	specs := []ui.Group{{ID: "identity"}, {ID: "profile"}}

	for _, g := range s.Groups(specs) {
		for _, f := range g.Fields {
			if f == "orphan" {
				t.Errorf("orphan landed in section %q despite the typo", g.ID)
			}
		}
	}
	stray := s.UndeclaredGroups(specs)
	if !reflect.DeepEqual(stray, map[string][]string{"typo": {"orphan"}}) {
		t.Errorf("UndeclaredGroups() = %v, want typo→[orphan]", stray)
	}
	// A clean declaration gives no false positives, or the gate would be
	// permanently red and useless.
	if len(sample().UndeclaredGroups(specs)) != 0 {
		t.Errorf("UndeclaredGroups() on a clean set = %v, want empty", sample().UndeclaredGroups(specs))
	}
}
