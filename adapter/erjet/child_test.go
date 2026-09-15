package erjet_test

import (
	"context"
	"strings"
	"testing"

	"github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5"

	"github.com/qrotux/editrig-go/adapter/erjet"
	"github.com/qrotux/editrig-go/decl"
	"github.com/qrotux/editrig-go/ui"
)

// The columns are standalone (not passed as columns of NewTable), so they carry
// no table name and go-jet renders them bare ("_order", not
// "trips_gallery._order"). Generated tables render columns with the table, and
// then ORDER BY reads "trips_gallery._order ASC", which no longer contains the
// substring "_order ASC, id ASC": moving the SQL assertions below onto generated
// columns means adjusting the substrings, not treating the tests as broken.
var (
	childSrc    = postgres.NewTable("public", "trips_gallery", "")
	childID     = postgres.StringColumn("id")
	childParent = postgres.StringColumn("_parent_id")
	childOrder  = postgres.IntegerColumn("_order")
	childKey    = postgres.StringColumn("_locale")
	childValue  = postgres.StringColumn("image_id")
)

func galleryChild() erjet.ChildTable {
	return erjet.ChildTable{
		Src: childSrc, ID: childID, Parent: childParent, Order: childOrder,
		ParentType: "uuid", NewID: func() string { return "generated" },
	}
}

// TestChildRowsSQLOrdersByPositionThenID: the secondary sort key is not
// decoration. _order can be NULL in legacy data, and without it the order of
// such rows would change from query to query, so the form would show the
// elements in a new order every time.
func TestChildRowsSQLOrdersByPositionThenID(t *testing.T) {
	sql, _ := erjet.ExportChildSelectSQL(galleryChild(), []postgres.Projection{childValue}, "p-1", "")
	// go-jet v2.15.0 does not quote lowercase snake_case identifiers that are
	// not reserved words, so the one substring "_order ASC, id ASC" checks both
	// the presence of the secondary key and the order (position first, then
	// id) without guessing about quotes.
	if !strings.Contains(sql, "ORDER BY") || !strings.Contains(sql, "_order ASC, id ASC") {
		t.Errorf("SELECT does not order by position then id:\n%s", sql)
	}
	if !strings.Contains(sql, "::text") {
		t.Errorf("parent is not compared through ::text (garbage in the id would 500 instead of 404):\n%s", sql)
	}
	if strings.Contains(sql, "_locale") {
		t.Errorf("no key partition was declared, yet the filter mentions one:\n%s", sql)
	}
}

// TestChildRowsSQLPartitions: on a partitioned table the key must be compared
// in its own type (text does not match an enum column), the same reason the
// satellites require KeyType.
func TestChildRowsSQLPartitions(t *testing.T) {
	ct := galleryChild()
	ct.Key, ct.KeyType = childKey, "public._locales"
	sql, _ := erjet.ExportChildSelectSQL(ct, []postgres.Projection{childValue}, "p-1", "en")
	// Separate Contains checks on "_locale" and "public._locales" would not
	// notice the cast hung on the wrong column (the parent instead of the key)
	// while the partition went unfiltered. One adjacent substring pins column,
	// operator and cast as a whole.
	if !strings.Contains(sql, "_locale = $2::text::public._locales") {
		t.Errorf("key filter is missing, uncast, or cast onto the wrong column:\n%s", sql)
	}
}

// TestChildValuesReplaceSQL: a replacement, not a diff, with the same caveat as
// ClearDeleteRow (suitable only if nothing references this table's ids). The
// query shape is pinned here; execution is covered by the conformance suite on
// a live database.
//
// _order is checked by argument, not by SQL substring: the substring "1" is
// also in "$1" (the first placeholder) regardless of what is bound, so a
// substring check would pass even with a 0-based bug.
func TestChildValuesReplaceSQL(t *testing.T) {
	ct := galleryChild()
	del, delArgs := erjet.ExportChildDeleteAllSQL(ct, "p-1", "")
	if !strings.HasPrefix(strings.TrimSpace(del), "DELETE") || !strings.Contains(del, "::text") {
		t.Errorf("delete is not scoped to the parent through ::text:\n%s", del)
	}
	if !containsArg(delArgs, "p-1") {
		t.Errorf("delete args do not carry the parent id: %#v", delArgs)
	}

	ins, insArgs := erjet.ExportChildInsertValueSQL(ct, erjet.UUID(childValue), "p-1", "", "img-1", 0)
	if !strings.Contains(ins, "INSERT") || !strings.Contains(ins, "uuid") {
		t.Errorf("insert lost the parent cast or the value cast:\n%s", ins)
	}
	if !containsArg(insArgs, int64(1)) {
		t.Errorf("_order must be 1-based (writeOrder(0) -> 1), args = %#v", insArgs)
	}
	if containsArg(insArgs, int64(0)) {
		t.Errorf("_order leaked a 0-based value, args = %#v", insArgs)
	}

	// Pinning only i=0 would not tell 1-based from a constant: a writeOrder that
	// always returns 1 would pass the check above too.
	_, ins2Args := erjet.ExportChildInsertValueSQL(ct, erjet.UUID(childValue), "p-1", "", "img-2", 1)
	if !containsArg(ins2Args, int64(2)) {
		t.Errorf("second element's _order must be 2 (writeOrder(1) -> 2), args = %#v", ins2Args)
	}
}

// TestChildValuesDeleteScopedToPartition: the DELETE half of the replacement is
// the one statement in this file able to erase another parent's or another
// partition's data. Both the shape (the same "_locale = $2::text::public._locales"
// pattern as the SELECT in TestChildRowsSQLPartitions) and the argument are
// pinned, not merely the presence of the word DELETE.
func TestChildValuesDeleteScopedToPartition(t *testing.T) {
	ct := galleryChild()
	ct.Key, ct.KeyType = childKey, "public._locales"
	del, args := erjet.ExportChildDeleteAllSQL(ct, "p-1", "en")
	if !strings.Contains(del, "_locale = $2::text::public._locales") {
		t.Errorf("delete does not scope to its own partition:\n%s", del)
	}
	if !containsArg(args, "en") {
		t.Errorf("delete args do not carry the key value: %#v", args)
	}
}

// TestChildValuesInsertOmitsKeyWhenUnpartitioned: a table without Key must not
// carry the key column in the insert at all, or a NOT NULL partition column
// would receive "" instead of a real locale and fail on a live database rather
// than in a test.
func TestChildValuesInsertOmitsKeyWhenUnpartitioned(t *testing.T) {
	ct := galleryChild()
	ins, _ := erjet.ExportChildInsertValueSQL(ct, erjet.UUID(childValue), "p-1", "", "img-1", 0)
	if strings.Contains(ins, "_locale") {
		t.Errorf("no key partition was declared, yet the insert mentions one:\n%s", ins)
	}
}

// TestChildValuesInsertCastsKeyForPartitionedTable: a partitioned insert must
// carry the key in its own type, as the read filter does
// (TestChildRowsSQLPartitions).
func TestChildValuesInsertCastsKeyForPartitionedTable(t *testing.T) {
	ct := galleryChild()
	ct.Key, ct.KeyType = childKey, "public._locales"
	ins, args := erjet.ExportChildInsertValueSQL(ct, erjet.UUID(childValue), "p-1", "en", "img-1", 0)
	if !strings.Contains(ins, "_locale") || !strings.Contains(ins, "::public._locales") {
		t.Errorf("insert does not carry a cast key column:\n%s", ins)
	}
	if !containsArg(args, "en") {
		t.Errorf("insert args do not carry the key value: %#v", args)
	}
}

// TestChildValuesWire: on the wire, an array of one column's values whose
// element shape is the argument cell's. (The behavioural "not a list means
// nothing written" check is TestChildValuesSaveRejectsNonList in
// child_internal_test.go, where a fakeTx lets the assertion actually run
// cell.save rather than only read the wire.)
func TestChildValuesWire(t *testing.T) {
	cell := erjet.ChildValues(galleryChild(), erjet.UUID(childValue))
	if got := erjet.ExportWire(cell); got.Kind != ui.KindList || got.Elem != ui.KindString {
		t.Errorf("wire = %+v, want list of strings", got)
	}
}

// TestChildValuesPanicsOnPartitionedTable: ChildValues on a table with Key is
// exactly the combination that silently merges partitions on read and wipes
// them all on an empty save (see the ChildValues doc). The gate must fail at
// declaration time, before the first form.
func TestChildValuesPanicsOnPartitionedTable(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("ChildValues on a partitioned ChildTable (Key set) did not panic")
		}
	}()
	ct := galleryChild()
	ct.Key, ct.KeyType = childKey, "public._locales"
	erjet.ChildValues(ct, erjet.UUID(childValue))
}

// TestKeyedChildValuesPanicsOnUnpartitionedTable: the mirror gate.
// KeyedChildValues without Key would put a nil projection into the read path's
// SELECT (readKeyedValues).
func TestKeyedChildValuesPanicsOnUnpartitionedTable(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("KeyedChildValues on an unpartitioned ChildTable (Key nil) did not panic")
		}
	}()
	erjet.KeyedChildValues(galleryChild(), erjet.UUID(childValue))
}

// TestKeyedChildValuesWire: on the wire, a map of lists whose element shape is
// the argument cell's.
func TestKeyedChildValuesWire(t *testing.T) {
	ct := galleryChild()
	ct.Key, ct.KeyType = childKey, "public._locales"
	cell := erjet.KeyedChildValues(ct, erjet.Str(childValue, ""))
	got := erjet.ExportWire(cell)
	if got.Kind != ui.KindMap || got.Elem != ui.KindList {
		t.Errorf("wire = %+v, want map of lists", got)
	}
}

// TestChildValuesPanicsWithoutValue: a cell with value==nil (ReadOnly, JSON,
// Deferred; JSONEditable has a value, see
// TestChildRowsAcceptsJSONEditableElementCell) must fail loudly at declaration
// time rather than silently not write on save, or the hole would only be found
// on a live form.
func TestChildValuesPanicsWithoutValue(t *testing.T) {
	defer wantPanicContaining(t, "ChildValues with a valueless cell (ReadOnly)",
		"erjet.Deferred", "second phase")()
	erjet.ChildValues(galleryChild(), erjet.ReadOnly(childValue))
}

// TestKeyedChildValuesPanicsWithoutValue: the same gate on the map variant,
// with the same argument cell constructor.
func TestKeyedChildValuesPanicsWithoutValue(t *testing.T) {
	defer wantPanicContaining(t, "KeyedChildValues with a valueless cell (ReadOnly)",
		"erjet.Deferred", "second phase")()
	ct := galleryChild()
	ct.Key, ct.KeyType = childKey, "public._locales"
	erjet.KeyedChildValues(ct, erjet.ReadOnly(childValue))
}

// --- object projection: ChildRows / KeyedChildRows ----------------------------
//
// The element is synthetic: an id (read-only, travelling as an object field), a
// day number and a title. The table name is a label in the generated SQL, not a
// reference to a live schema. The columns are standalone again, so the note
// about bare names at the top of the file applies here too.
var (
	itinSrc    = postgres.NewTable("public", "trips_included", "")
	itinID     = postgres.StringColumn("id")
	itinParent = postgres.StringColumn("_parent_id")
	itinOrder  = postgres.IntegerColumn("_order")
	itinKey    = postgres.StringColumn("_locale")
	itinDay    = postgres.IntegerColumn("day")
	itinTitle  = postgres.StringColumn("title")
	itinTags   = postgres.StringArrayColumn("tags")
	itinMeta   = postgres.StringColumn("meta")
)

func itineraryChild() erjet.ChildTable {
	return erjet.ChildTable{
		Src: itinSrc, ID: itinID, Parent: itinParent, Order: itinOrder,
		ParentType: "uuid", NewID: func() string { return "generated" },
	}
}

func itineraryDecl() decl.Set[erjet.Column] {
	return decl.Set[erjet.Column]{
		{Name: "id", Col: erjet.ReadOnly(itinID), UI: ui.String().Readonly()},
		{Name: "day", Col: erjet.Int(itinDay), UI: ui.Integer()},
		{Name: "title", Col: erjet.Text(itinTitle), UI: ui.String()},
	}
}

// TestChildRowsWire: on the wire, an array of objects (ui.Items). Elem stays
// empty: the element composition is checked by a separate decl.Lint call over
// the nested declaration, not by the wire shape.
func TestChildRowsWire(t *testing.T) {
	got := erjet.ExportWire(erjet.ChildRows(itineraryChild(), itineraryDecl()))
	if got.Kind != ui.KindItems || got.Elem != "" {
		t.Errorf("wire = %+v, want {items}", got)
	}
}

// TestKeyedChildRowsWire: the partitioned variant is a map of object lists.
func TestKeyedChildRowsWire(t *testing.T) {
	ct := itineraryChild()
	ct.Key, ct.KeyType = itinKey, "public._locales"
	got := erjet.ExportWire(erjet.KeyedChildRows(ct, itineraryDecl()))
	if got.Kind != ui.KindMap || got.Elem != ui.KindItems {
		t.Errorf("wire = %+v, want map of items", got)
	}
}

// TestChildRowsKeepsForeignIDsOut: the id comes from the client (StripReadonly
// cuts only the top level) and the conflict target ON CONFLICT (id) is the
// global PK, while the DELETE is bounded by the parent. A forged id of another
// parent must become a new element, not rewrite the foreign row. No "my list
// survived" test would ever see this.
func TestChildRowsKeepsForeignIDsOut(t *testing.T) {
	owned := map[string]bool{"mine-1": true}
	got := erjet.ExportChildResolveIDs(itineraryChild(), "id", owned, []any{
		map[string]any{"id": "mine-1"},
		map[string]any{"id": "someone-elses"},
		map[string]any{},
	})
	if len(got) != 3 {
		t.Fatalf("resolveIDs = %v, want one id per element", got)
	}
	if got[0] != "mine-1" {
		t.Errorf("own id was not kept: %v", got)
	}
	if got[1] == "someone-elses" {
		t.Error("a foreign id reached the upsert — it would overwrite another parent's row")
	}
	if got[1] != "generated" || got[2] != "generated" {
		t.Errorf("new elements did not get a fresh id: %v", got)
	}
}

// TestChildRowsResolveIDsRejectsRepeatedID: the same own id submitted twice is
// a forgery too: two upserts on one PK would collapse two form elements into
// one row, the second silently eating the first (its position, its values).
// The second occurrence must degrade to a new element, like a foreign id.
func TestChildRowsResolveIDsRejectsRepeatedID(t *testing.T) {
	owned := map[string]bool{"mine-1": true}
	got := erjet.ExportChildResolveIDs(itineraryChild(), "id", owned, []any{
		map[string]any{"id": "mine-1"},
		map[string]any{"id": "mine-1"},
	})
	if got[0] != "mine-1" {
		t.Errorf("first occurrence of an owned id must be kept: %v", got)
	}
	if got[1] != "generated" {
		t.Errorf("second occurrence of the same id must become a new element, got %v", got)
	}
}

// TestChildRowsResolveIDsReadsTheDeclaredIDField: the id field name comes from
// the declaration (the cell projected onto ct.ID), not a hard-coded "id". Hard-
// coded, a declaration naming the field differently would lose every row's
// identity on every save: all elements would look new, the old rows would be
// deleted, and their translations would cascade away.
func TestChildRowsResolveIDsReadsTheDeclaredIDField(t *testing.T) {
	owned := map[string]bool{"mine-1": true}
	got := erjet.ExportChildResolveIDs(itineraryChild(), "row_id", owned, []any{
		map[string]any{"row_id": "mine-1"},
	})
	if got[0] != "mine-1" {
		t.Errorf("resolveIDs ignored the declared id field name: %v", got)
	}
}

// TestChildRowsDeleteGuardHandlesEmptyList: an empty list is legitimate input
// (every element was removed). NOT IN with an empty set is a syntax error, and
// without the early branch the query would not build.
func TestChildRowsDeleteGuardHandlesEmptyList(t *testing.T) {
	sql, _ := erjet.ExportChildDeleteExceptSQL(itineraryChild(), "p-1", "", nil)
	if strings.Contains(sql, "NOT IN") {
		t.Errorf("empty keep-list must not produce NOT IN:\n%s", sql)
	}
	if !strings.HasPrefix(strings.TrimSpace(sql), "DELETE") || !strings.Contains(sql, "::text") {
		t.Errorf("empty keep-list must still delete the whole partition, scoped to the parent:\n%s", sql)
	}
	sql, args := erjet.ExportChildDeleteExceptSQL(itineraryChild(), "p-1", "", []string{"a", "b"})
	if !strings.Contains(sql, "NOT IN") {
		t.Errorf("non-empty keep-list must guard the delete:\n%s", sql)
	}
	// Arguments, not the SQL text: the text holds only placeholders, and "the
	// second id was never bound" is visible only here.
	if !containsArg(args, "p-1") || !containsArg(args, "a") || !containsArg(args, "b") {
		t.Errorf("delete args must carry the parent and every kept id: %#v", args)
	}
}

// TestChildRowsDeleteExceptScopedToPartition: on a partitioned table the DELETE
// of "extra" rows must stay within its own partition, or saving one locale
// would wipe the rows of every other (their ids are not in this partition's
// keep-list).
func TestChildRowsDeleteExceptScopedToPartition(t *testing.T) {
	ct := itineraryChild()
	ct.Key, ct.KeyType = itinKey, "public._locales"
	sql, args := erjet.ExportChildDeleteExceptSQL(ct, "p-1", "en", []string{"a"})
	if !strings.Contains(sql, "_locale = $2::text::public._locales") {
		t.Errorf("delete does not scope to its own partition:\n%s", sql)
	}
	if !containsArg(args, "en") {
		t.Errorf("delete args do not carry the key value: %#v", args)
	}
}

// TestChildRowsUpsertSQL: the whole upsert shape, columns, conflict target and
// SET.
//
// Four separate things are checked because each catches its own class of
// breakage: the column list (id and parent must be in the insert), the conflict
// target (without it this is a plain insert failing on a duplicate PK), the SET
// composition (without _order reordering is lost, without day a field edit is
// lost) and the 1-based position as an argument, not a substring.
func TestChildRowsUpsertSQL(t *testing.T) {
	sql, args := erjet.ExportChildUpsertRowSQL(itineraryChild(), itineraryDecl(),
		"p-1", "", "row-1", 0, map[string]any{"id": "row-1", "day": float64(2), "title": "Day two"})

	for _, want := range []string{
		"INSERT INTO public.trips_included (id, _parent_id, _order, day, title)",
		"ON CONFLICT (id) DO UPDATE",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("upsert is missing %q:\n%s", want, sql)
		}
	}
	// SET is checked per column: a DO UPDATE that forgot _order would leave
	// reordering silently unsaved, the list round-trips with the old position.
	for _, want := range []string{"_order = ", "day = ", "title = "} {
		if !strings.Contains(sql[strings.Index(sql, "DO UPDATE"):], want) {
			t.Errorf("DO UPDATE SET is missing %q:\n%s", want, sql)
		}
	}
	if !containsArg(args, "row-1") || !containsArg(args, "p-1") {
		t.Errorf("upsert args must carry the row id and the parent id: %#v", args)
	}
	if !containsArg(args, int64(1)) || containsArg(args, int64(0)) {
		t.Errorf("_order must be 1-based (writeOrder(0) -> 1), args = %#v", args)
	}
	// Values from SET specifically, not "somewhere among the args": _order is
	// bound twice (VALUES and SET) and containsArg does not tell them apart, so
	// a constant in SET would pass thanks to VALUES (see ExportAssignedArg). All
	// three assignments are checked: DO UPDATE is the only path for an existing
	// row.
	assertSetArg(t, sql, args, "_order", int64(1))
	assertSetArg(t, sql, args, "day", int64(2))
	assertSetArg(t, sql, args, "title", "Day two")

	// Pinning only pos=0 would not tell 1-based from a constant.
	sql2, args2 := erjet.ExportChildUpsertRowSQL(itineraryChild(), itineraryDecl(),
		"p-1", "", "row-2", 1, map[string]any{"id": "row-2"})
	if !containsArg(args2, int64(2)) {
		t.Errorf("second element's _order must be 2 (writeOrder(1) -> 2), args = %#v", args2)
	}
	assertSetArg(t, sql2, args2, "_order", int64(2))
}

// assertSetArg asserts that DO UPDATE assigns exactly want to col; a wrapper
// over ExportAssignedArg so the failure message names the column.
func assertSetArg(t *testing.T, sql string, args []any, col string, want any) {
	t.Helper()
	got, ok := erjet.ExportAssignedArg(sql, args, col)
	if !ok {
		t.Errorf("DO UPDATE does not assign %s from a parameter:\n%s", col, sql)
		return
	}
	if got != want {
		t.Errorf("DO UPDATE SET %s bound %#v, want %#v (the VALUES half can carry the right value while SET carries a wrong one):\n%s",
			col, got, want, sql)
	}
}

// TestChildRowsUpsertOmitsAbsentColumns: "not sent means untouched" holds
// inside an element too. Written unconditionally, a key absent from the payload
// would travel into the INSERT as zero/NULL and zero the column on every save
// of a neighbouring field.
func TestChildRowsUpsertOmitsAbsentColumns(t *testing.T) {
	sql, _ := erjet.ExportChildUpsertRowSQL(itineraryChild(), itineraryDecl(),
		"p-1", "", "row-1", 0, map[string]any{"id": "row-1", "day": float64(2)})
	if strings.Contains(sql, "title") {
		t.Errorf("a column absent from the payload reached the upsert:\n%s", sql)
	}
	if !strings.Contains(sql, "day") {
		t.Errorf("a column present in the payload is missing from the upsert:\n%s", sql)
	}
}

// TestChildRowsUpsertWithoutIDIsAPlainInsert: id=="" happens only on a table
// without NewID (a PK with DEFAULT gen_random_uuid()). There is nothing to
// conflict on: ON CONFLICT (id) with an uninserted id column would compare
// NULL and insert a duplicate anyway, so the insert must be a plain one.
func TestChildRowsUpsertWithoutIDIsAPlainInsert(t *testing.T) {
	ct := itineraryChild()
	ct.NewID = nil
	sql, _ := erjet.ExportChildUpsertRowSQL(ct, itineraryDecl(),
		"p-1", "", "", 0, map[string]any{"day": float64(1)})
	if strings.Contains(sql, "ON CONFLICT") {
		t.Errorf("an element with no id must not carry a conflict target:\n%s", sql)
	}
	if !strings.Contains(sql, "INSERT INTO public.trips_included (_parent_id, _order, day)") {
		t.Errorf("insert columns are wrong for an id-less element:\n%s", sql)
	}
}

// TestChildRowsUpsertCastsKeyForPartitionedTable: a partitioned insert must
// carry the key in its own type, as the read filter does.
func TestChildRowsUpsertCastsKeyForPartitionedTable(t *testing.T) {
	ct := itineraryChild()
	ct.Key, ct.KeyType = itinKey, "public._locales"
	sql, args := erjet.ExportChildUpsertRowSQL(ct, itineraryDecl(),
		"p-1", "en", "row-1", 0, map[string]any{"id": "row-1"})
	if !strings.Contains(sql, "_locale") || !strings.Contains(sql, "::public._locales") {
		t.Errorf("upsert does not carry a cast key column:\n%s", sql)
	}
	if !containsArg(args, "en") {
		t.Errorf("upsert args do not carry the key value: %#v", args)
	}
}

// TestChildRowsUpsertCastsIDForNonTextPK: the id is compared through ::text
// (scope) but inserted into the column, and a uuid PK rejects a text parameter.
// That fails only on a live database, as a 500, so the cast is pinned here.
func TestChildRowsUpsertCastsIDForNonTextPK(t *testing.T) {
	ct := itineraryChild()
	ct.IDType = "uuid"
	sql, _ := erjet.ExportChildUpsertRowSQL(ct, itineraryDecl(),
		"p-1", "", "row-1", 0, map[string]any{"id": "row-1"})
	if !strings.Contains(sql, "$1::text::uuid") {
		t.Errorf("the inserted id is not cast to the PK's own type:\n%s", sql)
	}
}

// TestChildRowsPanicsOnPartitionedTable: ChildRows on a table with Key would
// read every partition as one list, and the DELETE of "extra" rows would wipe
// every other partition at once (their ids are not in the keep-list). The gate
// must fail at declaration time, before the first form, as ChildValues does.
func TestChildRowsPanicsOnPartitionedTable(t *testing.T) {
	defer wantPanic(t, "ChildRows on a partitioned ChildTable (Key set)")()
	ct := itineraryChild()
	ct.Key, ct.KeyType = itinKey, "public._locales"
	erjet.ChildRows(ct, itineraryDecl())
}

// TestKeyedChildRowsPanicsOnUnpartitionedTable: the mirror gate.
// KeyedChildRows without Key would put a nil projection into the read path's
// SELECT.
func TestKeyedChildRowsPanicsOnUnpartitionedTable(t *testing.T) {
	defer wantPanic(t, "KeyedChildRows on an unpartitioned ChildTable (Key nil)")()
	erjet.KeyedChildRows(itineraryChild(), itineraryDecl())
}

// TestChildRowsPanicsWithoutIDField: without a field projected onto ct.ID the
// id never reaches the form and comes back empty: every element would look
// new, the old rows would be deleted, and the cascade would take their
// translations. Silently: the list still round-trips.
func TestChildRowsPanicsWithoutIDField(t *testing.T) {
	defer wantPanic(t, "ChildRows without an id field in the element declaration")()
	erjet.ChildRows(itineraryChild(), decl.Set[erjet.Column]{
		{Name: "day", Col: erjet.Int(itinDay), UI: ui.Integer()},
	})
}

// TestChildRowsPanicsOnWritableIDField: a writable id lies to the form. The
// write ignores it (the id comes from ownedIDs/NewID, not from the cell), but
// decl.Lint would declare the field editable and the admin would see an input
// that does nothing.
func TestChildRowsPanicsOnWritableIDField(t *testing.T) {
	defer wantPanic(t, "ChildRows with a writable id field")()
	erjet.ChildRows(itineraryChild(), decl.Set[erjet.Column]{
		{Name: "id", Col: erjet.Str(itinID, ""), UI: ui.String()},
		{Name: "day", Col: erjet.Int(itinDay), UI: ui.Integer()},
	})
}

// TestChildRowsAcceptsJSONEditableElementCell: JSONEditable produces value with
// the same expression as assign (column.go), so an existing element's field
// updates and a new element's inserts. Both halves are present and
// childElement accepts the cell; with assign only, the column would work every
// other time (update kept, insert lost).
func TestChildRowsAcceptsJSONEditableElementCell(t *testing.T) {
	set := decl.Set[erjet.Column]{
		{Name: "id", Col: erjet.ReadOnly(itinID), UI: ui.String().Readonly()},
		{Name: "meta", Col: erjet.JSONEditable(itinMeta), UI: ui.JSON()},
	}
	_ = erjet.ChildRows(itineraryChild(), set)

	sql, args := erjet.ExportChildUpsertRowSQL(itineraryChild(), set,
		"p-1", "", "row-1", 0, map[string]any{"id": "row-1", "meta": map[string]any{"k": "v"}})
	idx := strings.Index(sql, "ON CONFLICT")
	if idx < 0 {
		t.Fatalf("upsert sql has no ON CONFLICT — expected an id-bearing upsert: sql=%q", sql)
	}
	insertHalf, updateHalf := sql[:idx], sql[idx:]
	if !strings.Contains(insertHalf, "meta") {
		t.Errorf("insert is missing %q:\n%s", "meta", sql)
	}
	if !strings.Contains(updateHalf, "meta") {
		t.Errorf("DO UPDATE is missing %q:\n%s", "meta", sql)
	}
	if len(args) == 0 {
		t.Fatalf("upsert with a JSONEditable element field produced no args: sql=%q args=%v", sql, args)
	}
}

// TestChildRowsPanicsOnDeferredCell: Deferred has assign but no value (it
// materializes in a second phase, after the parent row is inserted), and
// ChildRows must fail at declaration time rather than silently lose the field
// on new elements.
func TestChildRowsPanicsOnDeferredCell(t *testing.T) {
	defer wantPanicContaining(t, "ChildRows with a Deferred element cell",
		"erjet.Deferred", "second phase", "photo")()
	erjet.ChildRows(itineraryChild(), decl.Set[erjet.Column]{
		{Name: "id", Col: erjet.ReadOnly(itinID), UI: ui.String().Readonly()},
		{Name: "photo", Col: erjet.Deferred(itinTitle,
			func(context.Context, pgx.Tx, string, any) (any, error) { return nil, nil },
		), UI: ui.String()},
	})
}

// TestChildRowsPanicsOnIDFieldWithCustomDecode pins the ownership decoder gate.
func TestChildRowsPanicsOnIDFieldWithCustomDecode(t *testing.T) {
	defer wantPanic(t, "ChildRows with a JSON id field (custom decode)")()
	erjet.ChildRows(itineraryChild(), decl.Set[erjet.Column]{
		{Name: "id", Col: erjet.JSON(itinID), UI: ui.String().Readonly()},
		{Name: "day", Col: erjet.Int(itinDay), UI: ui.Integer()},
	})
}

// TestChildRowsAllowsReadOnlyElementColumns: the flip side of the gate above. A
// non-writable column (ReadOnly/JSON) inside an element is legal: it is read
// with the rest and simply takes no part in the write. Forbidding it would
// leave no way to declare a shown but uneditable element column.
func TestChildRowsAllowsReadOnlyElementColumns(t *testing.T) {
	erjet.ChildRows(itineraryChild(), decl.Set[erjet.Column]{
		{Name: "id", Col: erjet.ReadOnly(itinID), UI: ui.String().Readonly()},
		{Name: "title", Col: erjet.ReadOnly(itinTitle), UI: ui.String().Readonly()},
	})
}

// TestChildRowsPanicsOnSatelliteWithoutNewID: without NewID the database
// generates the element id and it is unknown here, so a new element's nested
// satellite would be written with parentID "" (orphan rows in the translations
// table) while an existing element's would work. A write that works every
// other time is worse than one that does not work.
func TestChildRowsPanicsOnSatelliteWithoutNewID(t *testing.T) {
	defer wantPanic(t, "ChildRows with a nested satellite and no NewID")()
	ct := itineraryChild()
	ct.NewID = nil
	erjet.ChildRows(ct, decl.Set[erjet.Column]{
		{Name: "id", Col: erjet.ReadOnly(itinID), UI: ui.String().Readonly()},
		{Name: "content", Col: erjet.KeyedStrings(
			erjet.KeyedTable{Src: itinSrc, Parent: itinParent, Key: itinKey},
			itinTitle, erjet.ClearSetNull(),
		), UI: ui.Keyed(ui.String(), ui.Keys("en"))},
	})
}

// TestChildConstructorsRequireTheirColumns: ID/Parent/Order must be checked at
// declaration time by all four constructors.
//
// An unset struct field compiles silently and fails as a nil interface inside
// go-jet: a ChildTable without Order crashes on ct.Order.ASC() on the first
// Load (or ct.Order.SET() on the first Save), without ID on ct.ID.ASC() in the
// same place, without Parent in scope(). All of it is a 500 on a live form,
// exactly the outcome the "declaration panics fire at declaration" invariant
// exists to prevent.
//
// The full matrix (4 constructors x 3 columns) rather than one case: "one
// constructor is covered" is precisely the kind of gap that gets the other
// three declared safe.
func TestChildConstructorsRequireTheirColumns(t *testing.T) {
	// Partitioned and unpartitioned shapes: each constructor has its own, and
	// handing it the wrong one would catch the Key gate instead of the column
	// gate.
	partitioned := func() erjet.ChildTable {
		ct := galleryChild()
		ct.Key, ct.KeyType = childKey, "public._locales"
		return ct
	}
	constructors := map[string]func(erjet.ChildTable){
		"ChildValues":      func(ct erjet.ChildTable) { erjet.ChildValues(ct, erjet.UUID(childValue)) },
		"KeyedChildValues": func(ct erjet.ChildTable) { erjet.KeyedChildValues(ct, erjet.Str(childValue, "")) },
		"ChildRows":        func(ct erjet.ChildTable) { erjet.ChildRows(ct, itineraryDecl()) },
		"KeyedChildRows":   func(ct erjet.ChildTable) { erjet.KeyedChildRows(ct, itineraryDecl()) },
	}
	needsKey := map[string]bool{"KeyedChildValues": true, "KeyedChildRows": true}
	blanks := map[string]func(*erjet.ChildTable){
		"ID":     func(ct *erjet.ChildTable) { ct.ID = nil },
		"Parent": func(ct *erjet.ChildTable) { ct.Parent = nil },
		"Order":  func(ct *erjet.ChildTable) { ct.Order = nil },
	}

	for _, ctor := range []string{"ChildValues", "KeyedChildValues", "ChildRows", "KeyedChildRows"} {
		for _, column := range []string{"ID", "Parent", "Order"} {
			t.Run(ctor+"/"+column, func(t *testing.T) {
				defer wantPanicContaining(t, ctor+" on a ChildTable with a nil "+column, "ChildTable."+column)()
				ct := galleryChild()
				if needsKey[ctor] {
					ct = partitioned()
				}
				// The object constructors read the element from itineraryDecl,
				// whose id is projected onto itinID: for NewTable to find it,
				// ct.ID must be that column, not the gallery one.
				if ctor == "ChildRows" || ctor == "KeyedChildRows" {
					ct.Src, ct.ID, ct.Parent, ct.Order = itinSrc, itinID, itinParent, itinOrder
				}
				blanks[column](&ct)
				constructors[ctor](ct)
			})
		}
	}
}

// TestChildValuesAcceptsArrayCell: an array cell with a value half is a legal
// element of the scalar projection, a text[] column in the child row.
func TestChildValuesAcceptsArrayCell(t *testing.T) {
	ct := galleryChild()
	cell := erjet.Strings(postgres.StringArrayColumn("value"))
	_ = erjet.ChildValues(ct, cell)

	sql, args := erjet.ExportChildInsertValueSQL(ct, cell, "p1", "", []any{"a", "b"}, 0)
	// The column and the array cast must be in the SQL itself: "there is an
	// INSERT and args are non-empty" would be green without them, since
	// parentID alone makes args non-empty.
	if !strings.Contains(sql, " value)") || !strings.Contains(sql, "::text[]") || len(args) == 0 {
		t.Fatalf("insert with an array element must target the array column: sql=%q args=%v", sql, args)
	}
}

// TestKeyedChildValuesAcceptsArrayCell: the same on the partitioned twin, with
// the fixture of TestChildValuesInsertCastsKeyForPartitionedTable.
func TestKeyedChildValuesAcceptsArrayCell(t *testing.T) {
	ct := galleryChild()
	ct.Key, ct.KeyType = childKey, "public._locales"
	cell := erjet.Strings(postgres.StringArrayColumn("value"))
	_ = erjet.KeyedChildValues(ct, cell)

	sql, args := erjet.ExportChildInsertValueSQL(ct, cell, "p1", "en", []any{"a", "b"}, 0)
	// Same caveat as the unpartitioned twin above: without checking the column
	// and the cast the assertion stays green even with the array missing from
	// VALUES.
	if !strings.Contains(sql, " value)") || !strings.Contains(sql, "::text[]") || len(args) == 0 {
		t.Fatalf("insert with an array element must target the array column: sql=%q args=%v", sql, args)
	}
}

// TestChildRowsAcceptsArrayAndJSONCells: an element declaring both an array
// cell and JSONEditable, both carrying a value half, passes childElement, and
// ExportChildUpsertRowSQL must put both fields into the insert and into DO
// UPDATE. If the halves diverged in column composition, the field would save
// only for new elements (or only for existing ones), silently.
func TestChildRowsAcceptsArrayAndJSONCells(t *testing.T) {
	set := decl.Set[erjet.Column]{
		{Name: "id", Col: erjet.ReadOnly(itinID), UI: ui.String().Readonly()},
		{Name: "tags", Col: erjet.Strings(itinTags), UI: ui.List(ui.String())},
		{Name: "meta", Col: erjet.JSONEditable(itinMeta), UI: ui.JSON()},
	}
	_ = erjet.ChildRows(itineraryChild(), set)

	sql, args := erjet.ExportChildUpsertRowSQL(itineraryChild(), set,
		"p-1", "", "row-1", 0, map[string]any{
			"id": "row-1", "tags": []any{"a", "b"}, "meta": map[string]any{"k": "v"},
		})
	conflictIdx := strings.Index(sql, "ON CONFLICT")
	updateIdx := strings.Index(sql, "DO UPDATE")
	if conflictIdx < 0 || updateIdx < 0 {
		t.Fatalf("upsert sql has no ON CONFLICT/DO UPDATE — expected an id-bearing upsert: sql=%q", sql)
	}
	insertHalf := sql[:conflictIdx]
	updateHalf := sql[updateIdx:]
	for _, want := range []string{"tags", "meta"} {
		if !strings.Contains(insertHalf, want) {
			t.Errorf("insert is missing %q:\n%s", want, sql)
		}
		if !strings.Contains(updateHalf, want) {
			t.Errorf("DO UPDATE is missing %q:\n%s", want, sql)
		}
	}
	if len(args) == 0 {
		t.Fatal("upsert produced no args")
	}
}

// wantPanic is the shared tail of the gate tests: the constructor must fail at
// declaration time. Called through defer, so the message describes the input,
// not the expectation.
func wantPanic(t *testing.T, what string) func() {
	t.Helper()
	return func() {
		if recover() == nil {
			t.Fatalf("%s did not panic", what)
		}
	}
}

// wantPanicContaining is wantPanic with a check on the panic text.
//
// A separate helper rather than a stricter wantPanic: for most gates the panic
// itself is what matters, and pinning their wording would break tests on every
// rephrase. The text is pinned only where wrong text was the defect (a message
// naming the wrong constructors) or where the name of the missing field is the
// only thing telling three neighbouring panics apart.
func wantPanicContaining(t *testing.T, what string, substrings ...string) func() {
	t.Helper()
	return func() {
		r := recover()
		if r == nil {
			t.Fatalf("%s did not panic", what)
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("%s panicked with %T (%v), want a string message", what, r, r)
		}
		for _, want := range substrings {
			if !strings.Contains(msg, want) {
				t.Errorf("%s panicked with %q, which does not mention %q — the message must name the actual "+
					"condition and the cells that hit it, or it sends the author looking for the wrong mistake",
					what, msg, want)
			}
		}
	}
}

// containsArg reports whether a query argument equals want. Direct == rather
// than fmt.Sprint: the type matters too, int64(0) and int64(1) must differ
// rather than coincide in text form.
func containsArg(args []any, want any) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
