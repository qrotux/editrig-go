package erjet

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5"

	"github.com/qrotux/editrig-go/decl"
)

// Internal test file (package erjet, not erjet_test): rows is unexported and
// reachable only from here, as TestRelsLoad*/TestRelsSave* in rels_test.go
// reach RelsTable.read/replace. recQuerier/multiRows from there are reused:
// same contract (Querier), same meaning of the fake.

var (
	icID     = postgres.StringColumn("id")
	icParent = postgres.StringColumn("_parent_id")
	icOrder  = postgres.IntegerColumn("_order")
	icKey    = postgres.StringColumn("_locale")
	icValue  = postgres.StringColumn("image_id")
	icSrc    = postgres.NewTable("public", "trips_gallery", "")
)

func testChildTable() ChildTable {
	return ChildTable{Src: icSrc, ID: icID, Parent: icParent, Order: icOrder, ParentType: "uuid"}
}

// TestChildSelectSQLPanicsOnEmptyProjections: postgres.SELECT cannot be built
// without at least one projection, so an empty element declaration must panic
// with a clear message rather than a bare index out of range on projs[0].
func TestChildSelectSQLPanicsOnEmptyProjections(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("selectSQL with no projections did not panic")
		}
	}()
	testChildTable().selectSQL(nil, "p-1", "")
}

// TestChildRowsEmptyIsArray: the form field is a list, and nil would reach the
// widget as null. Pins the very regression "out := [][]any{}" in rows exists
// for; a "simplification" to `var out [][]any` must turn this test red.
func TestChildRowsEmptyIsArray(t *testing.T) {
	ct := testChildTable()
	got, err := ct.rows(context.Background(), &recQuerier{}, []postgres.Projection{icValue}, "p-1", "")
	if err != nil {
		t.Fatalf("rows: %v", err)
	}
	if got == nil {
		t.Fatal("rows returned nil, want [][]any{} — a form field is a list, and nil reaches the widget as null")
	}
	if len(got) != 0 {
		t.Fatalf("rows = %#v, want empty", got)
	}
}

// errQuerier fails Query before the first row (network down, connection
// expired), the earliest of the three failures a read can produce.
type errQuerier struct{ err error }

func (q *errQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, q.err
}

// TestChildRowsQueryErrorPropagates: a Query failure must reach the caller as
// is, not get lost in an empty result.
func TestChildRowsQueryErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	ct := testChildTable()
	_, err := ct.rows(context.Background(), &errQuerier{err: boom}, []postgres.Projection{icValue}, "p-1", "")
	if !errors.Is(err, boom) {
		t.Fatalf("rows err = %v, want boom", err)
	}
}

// valuesErrRows has a row (Next() says true) that cannot be read: a failure in
// the middle of the read rather than before it, the second error branch in
// rows.
type valuesErrRows struct {
	pgx.Rows
	err  error
	done bool
}

func (r *valuesErrRows) Next() bool {
	if r.done {
		return false
	}
	r.done = true
	return true
}
func (r *valuesErrRows) Values() ([]any, error) { return nil, r.err }
func (r *valuesErrRows) Close()                 {}
func (r *valuesErrRows) Err() error             { return nil }

// exhaustedErrRows has no rows at all (Next() is false at once), but the
// connection broke in a way visible only through Err() after the loop: exactly
// the `return out, rows.Err()` branch at the end of rows, which neither
// errQuerier nor valuesErrRows touches.
type exhaustedErrRows struct {
	pgx.Rows
	err error
}

func (r *exhaustedErrRows) Next() bool             { return false }
func (r *exhaustedErrRows) Values() ([]any, error) { return nil, nil }
func (r *exhaustedErrRows) Close()                 {}
func (r *exhaustedErrRows) Err() error             { return r.err }

// fixedRowsQuerier always returns a prepared Rows: for tests where the Rows
// behaviour matters (valuesErrRows/exhaustedErrRows), not what Query returns.
type fixedRowsQuerier struct{ rows pgx.Rows }

func (q *fixedRowsQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return q.rows, nil
}

// TestChildRowsValuesErrorPropagates: a Values() failure mid-read must stop the
// loop and reach the caller, not quietly return a partial list.
func TestChildRowsValuesErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	ct := testChildTable()
	q := &fixedRowsQuerier{rows: &valuesErrRows{err: boom}}
	_, err := ct.rows(context.Background(), q, []postgres.Projection{icValue}, "p-1", "")
	if !errors.Is(err, boom) {
		t.Fatalf("rows err = %v, want boom", err)
	}
}

// TestChildRowsErrPropagatesAfterExhaustion: rows.Err() after an exhausted loop
// must reach the caller; a loop with no rows does not mean "all good" when the
// read itself broke.
func TestChildRowsErrPropagatesAfterExhaustion(t *testing.T) {
	boom := errors.New("boom")
	ct := testChildTable()
	q := &fixedRowsQuerier{rows: &exhaustedErrRows{err: boom}}
	_, err := ct.rows(context.Background(), q, []postgres.Projection{icValue}, "p-1", "")
	if !errors.Is(err, boom) {
		t.Fatalf("rows err = %v, want boom", err)
	}
}

// TestKeyedChildValuesSaveSkipsPartitionsIndependently: fakeTx (store_test.go)
// checks behaviour, not only SQL shape. The ExportChild*SQL hatches in
// child_test.go pin the shape of one query; here the point is that the loop
// over the patch does not drop neighbouring partitions and does not touch what
// the patch omits. "ru" is garbage (not a list), "en"/"de" are real
// partitions: exactly one DELETE each for "en" and "de" (garbage "ru" gets no
// DELETE at all, the "erasing would be the worst outcome" case) and one INSERT
// per element.
func TestKeyedChildValuesSaveSkipsPartitionsIndependently(t *testing.T) {
	ct := testChildTable()
	ct.Key, ct.KeyType = icKey, "public._locales"
	cell := KeyedChildValues(ct, UUID(icValue))

	tx := &fakeTx{}
	patch := map[string]any{
		"en": []any{"img-1", "img-2"}, // partition: 1 DELETE + 2 INSERT
		"ru": "not-a-list",            // garbage: the partition is left alone
		"de": []any{"img-3"},          // partition: 1 DELETE + 1 INSERT
	}
	if _, err := cell.save(context.Background(), tx, "p-1", patch); err != nil {
		t.Fatalf("save: %v", err)
	}

	var deletes, inserts int
	for _, sql := range tx.execs {
		switch {
		case strings.HasPrefix(strings.TrimSpace(sql), "DELETE"):
			deletes++
		case strings.Contains(sql, "INSERT"):
			inserts++
		default:
			t.Errorf("unexpected exec: %s", sql)
		}
	}
	if deletes != 2 {
		t.Errorf("deletes = %d, want 2 (one per REAL partition; garbage \"ru\" must not delete anything)", deletes)
	}
	if inserts != 3 {
		t.Errorf("inserts = %d, want 3 (2 for en + 1 for de)", inserts)
	}
}

// TestKeyedChildValuesSaveDeletesScopedToOwnPartition: a white-box test on
// replaceValues. The DELETE is the one statement here able to erase data, and
// the previous test counts statements without looking at what is bound, so it
// would pass a bug where replaceValues always calls deleteAllSQL(parentID, "")
// instead of (parentID, key): the SQL text is the same for "en" and "de", only
// the args differ. fakeTx.execArgs (store_test.go) records them; the DELETE of
// partition "de" must carry "de" and not "en", and vice versa.
func TestKeyedChildValuesSaveDeletesScopedToOwnPartition(t *testing.T) {
	ct := testChildTable()
	ct.Key, ct.KeyType = icKey, "public._locales"
	cell := KeyedChildValues(ct, UUID(icValue))

	tx := &fakeTx{}
	patch := map[string]any{
		"en": []any{"img-1"},
		"de": []any{"img-2"},
	}
	if _, err := cell.save(context.Background(), tx, "p-1", patch); err != nil {
		t.Fatalf("save: %v", err)
	}

	var deletes [][]any
	for i, sql := range tx.execs {
		if strings.HasPrefix(strings.TrimSpace(sql), "DELETE") {
			deletes = append(deletes, tx.execArgs[i])
		}
	}
	// sortedMapKeys walks the patch alphabetically: "de" before "en".
	if len(deletes) != 2 {
		t.Fatalf("want 2 DELETEs (one per partition), got %d: %#v", len(deletes), deletes)
	}
	if !argsContain(deletes[0], "de") || argsContain(deletes[0], "en") {
		t.Errorf("first delete (partition \"de\") args = %#v, want to carry \"de\" only", deletes[0])
	}
	if !argsContain(deletes[1], "en") || argsContain(deletes[1], "de") {
		t.Errorf("second delete (partition \"en\") args = %#v, want to carry \"en\" only", deletes[1])
	}
}

func argsContain(args []any, want any) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// TestChildValuesSaveRejectsNonList: not a list means nothing parsed, nothing
// to write. A behavioural check, not only the wire shape (TestChildValuesWire
// in child_test.go): cell.save actually runs with a string payload, and the
// assertion is that no statement reached the tx. Without that branch a string
// payload would wipe the whole list with one DELETE.
func TestChildValuesSaveRejectsNonList(t *testing.T) {
	ct := testChildTable()
	cell := ChildValues(ct, UUID(icValue))

	tx := &fakeTx{}
	got, err := cell.save(context.Background(), tx, "p-1", "not-a-list")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if got != nil {
		t.Errorf("save returned %#v, want nil (nothing was written)", got)
	}
	if len(tx.execs) != 0 {
		t.Errorf("save executed %d statement(s) for a non-list payload, want 0: %#v", len(tx.execs), tx.execs)
	}
}

// --- object projection: write behaviour ---------------------------------------
//
// Here rather than in child_test.go because the tx and Querier doubles are
// needed: the Export*SQL hatches pin the shape of one query from outside, while
// these tests care how many statements went out, in which order, and what was
// bound in them.

var (
	itID     = postgres.StringColumn("id")
	itParent = postgres.StringColumn("_parent_id")
	itOrder  = postgres.IntegerColumn("_order")
	itKey    = postgres.StringColumn("_locale")
	itDay    = postgres.IntegerColumn("day")
	itTitle  = postgres.StringColumn("title")
	// A synthetic fixture: table and column names are only labels in the
	// generated SQL. The shape is real, though: an element with a varchar id
	// and _order plus a satellite whose _parent_id references the element's id
	// with ON DELETE CASCADE.
	itSrc        = postgres.NewTable("public", "trips_included", "")
	itLocSrc     = postgres.NewTable("public", "trips_included_locales", "")
	itLocParent  = postgres.StringColumn("_parent_id")
	itLocKey     = postgres.StringColumn("_locale")
	itLocContent = postgres.StringColumn("content")
)

func itinTable() ChildTable {
	return ChildTable{
		Src: itSrc, ID: itID, Parent: itParent, Order: itOrder,
		ParentType: "uuid", NewID: func() string { return "generated" },
	}
}

func itinDecl() decl.Set[Column] {
	return decl.Set[Column]{
		{Name: "id", Col: ReadOnly(itID)},
		{Name: "day", Col: Int(itDay)},
		{Name: "title", Col: Text(itTitle)},
	}
}

// itinDeclWithSatellite is the same element plus a nested satellite: a map of
// translations living in its own table, parented on the element's id.
func itinDeclWithSatellite() decl.Set[Column] {
	return append(itinDecl(), Entry{Name: "content", Col: KeyedStrings(
		KeyedTable{Src: itLocSrc, Parent: itLocParent, Key: itLocKey, KeyType: "public._locales"},
		itLocContent, ClearSetNull(),
	)})
}

// Entry is an alias for the declaration entry, to keep the literals above short.
type Entry = decl.Entry[Column]

// scriptQ is a Querier/Tx with scripted answers, one row set per successive
// Query. fakeTx (store_test.go) returns exactly one row per query, while the
// object write needs many rows (ownedIDs) and different answers from call to
// call (ownership first, then the re-read). queryArgs[i] pairs with
// queries[i]: without them the double would return the scripted rows for any
// query, and "the satellite was read by the wrong id" would stay invisible
// (checked by mutation: c.load(ctx, q, parentID) passed the value assertions).
type scriptQ struct {
	fakeTx
	results   [][][]any
	queryArgs [][]any
}

func (q *scriptQ) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.queries = append(q.queries, sql)
	q.queryArgs = append(q.queryArgs, args)
	if len(q.results) == 0 {
		return &multiRows{}, nil
	}
	r := q.results[0]
	q.results = q.results[1:]
	return &multiRows{rows: r}, nil
}

// execsOf returns the args of the tx statements of one kind; the tests below
// assert both the count and the contents, so the split is shared.
func execsOf(tx *scriptQ, kind string) [][]any {
	var out [][]any
	for i, sql := range tx.execs {
		if strings.Contains(sql, kind) {
			out = append(out, tx.execArgs[i])
		}
	}
	return out
}

// TestChildRowsSaveUpsertsOwnAndInsertsForeignAsNew is the main test of the
// object write.
//
// The parent owns one row ("mine-1"). The form sends it back, plus an element
// with a foreign row's id, plus an element with no id at all. Three things are
// checked at once: the own row is upserted under its own id; the foreign id
// reaches no statement whatsoever (otherwise ON CONFLICT (id) would rewrite
// another parent's row, the conflict target is global while the DELETE is
// bounded by the parent); and the DELETE of "extra" rows is guarded by the
// keep-list rather than wiping the partition.
func TestChildRowsSaveUpsertsOwnAndInsertsForeignAsNew(t *testing.T) {
	cell := ChildRows(itinTable(), itinDecl())
	tx := &scriptQ{results: [][][]any{{{"mine-1"}}}} // ownedIDs: the parent has one row

	_, err := cell.save(context.Background(), tx, "p-1", []any{
		map[string]any{"id": "mine-1", "title": "kept"},
		map[string]any{"id": "someone-elses", "title": "forged"},
		map[string]any{"title": "brand new"},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	for i, args := range tx.execArgs {
		if argsContain(args, "someone-elses") {
			t.Errorf("a foreign id reached statement %d (%s) with args %#v — ON CONFLICT (id) would have rewritten another parent's row",
				i, tx.execs[i], args)
		}
	}

	deletes := execsOf(tx, "DELETE")
	if len(deletes) != 1 {
		t.Fatalf("want exactly 1 DELETE, got %d: %#v", len(deletes), deletes)
	}
	if !strings.Contains(tx.execs[0], "NOT IN") {
		t.Errorf("delete is not guarded by the keep-list — it would wipe rows the payload still holds:\n%s", tx.execs[0])
	}
	if !argsContain(deletes[0], "mine-1") {
		t.Errorf("kept id is missing from the delete guard: %#v", deletes[0])
	}

	upserts := execsOf(tx, "ON CONFLICT")
	if len(upserts) != 3 {
		t.Fatalf("want 3 upserts (one per element), got %d: %#v", len(upserts), tx.execs)
	}
	if !argsContain(upserts[0], "mine-1") {
		t.Errorf("own element was not upserted under its own id: %#v", upserts[0])
	}
	if !argsContain(upserts[1], "generated") || !argsContain(upserts[2], "generated") {
		t.Errorf("forged and id-less elements did not get a fresh id: %#v", upserts[1:])
	}
	// Positions 1..3 consecutively: the element with the forged id does not
	// drop out of the numbering, it is "added".
	for i, args := range upserts {
		if !argsContain(args, int64(i+1)) {
			t.Errorf("element %d got the wrong _order (want %d, 1-based): %#v", i, i+1, args)
		}
	}
}

// TestChildRowsSaveKeepsNestedSatelliteThroughAReorder is the second main test.
//
// The parent has two elements, each with translations in the fixture's side
// table (_parent_id -> element id, ON DELETE CASCADE). The form sends them in
// reverse order, exactly the scenario a replacement (DELETE+INSERT) would pass
// "green": elements present, values right, order right, and both elements'
// translations cascaded away.
//
// So the assertions are about identity, not the list: no row is deleted (the
// keep-list holds both ids), each upsert goes under its own id, and the nested
// satellite is written with parentID = the element's id and after its upsert,
// since before the parent row exists there is nowhere to insert a translation.
func TestChildRowsSaveKeepsNestedSatelliteThroughAReorder(t *testing.T) {
	cell := ChildRows(itinTable(), itinDeclWithSatellite())
	tx := &scriptQ{results: [][][]any{{{"e-1"}, {"e-2"}}}} // ownedIDs: both rows are ours

	_, err := cell.save(context.Background(), tx, "p-1", []any{
		map[string]any{"id": "e-2", "title": "second", "content": map[string]any{"en": "two"}},
		map[string]any{"id": "e-1", "title": "first", "content": map[string]any{"en": "one"}},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	deletes := execsOf(tx, "DELETE")
	if len(deletes) != 1 {
		t.Fatalf("want exactly 1 DELETE, got %d: %#v", len(deletes), tx.execs)
	}
	if !argsContain(deletes[0], "e-1") || !argsContain(deletes[0], "e-2") {
		t.Errorf("both submitted rows must be in the keep-list, or the cascade wipes their translations: %#v", deletes[0])
	}
	if argsContain(deletes[0], "generated") {
		t.Errorf("a resubmitted own id was treated as new — its row would be deleted and its translations cascade away: %#v", deletes[0])
	}

	// Statement order: element upsert, then its satellite, then the second
	// element, then its satellite. Compared by index, not by counts: "the
	// satellite was written before its row" is a separate breakage (inserting
	// a translation for a non-existent parent violates the foreign key).
	var kinds []string
	for _, sql := range tx.execs {
		switch {
		case strings.HasPrefix(strings.TrimSpace(sql), "DELETE"):
			kinds = append(kinds, "delete")
		case strings.Contains(sql, "trips_included_locales"):
			kinds = append(kinds, "satellite")
		default:
			kinds = append(kinds, "element")
		}
	}
	want := []string{"delete", "element", "satellite", "element", "satellite"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("statement order = %v, want %v\n%v", kinds, want, tx.execs)
	}

	// The satellite of the first submitted element must be parented on e-2
	// (the element's id), not on "p-1" (the entity's id) and not on the
	// neighbouring element's id.
	if !argsContain(tx.execArgs[2], "e-2") || argsContain(tx.execArgs[2], "p-1") {
		t.Errorf("nested satellite of element e-2 is parented wrong: %#v", tx.execArgs[2])
	}
	if !argsContain(tx.execArgs[4], "e-1") || argsContain(tx.execArgs[4], "p-1") {
		t.Errorf("nested satellite of element e-1 is parented wrong: %#v", tx.execArgs[4])
	}
	// The new order is really written: e-2 is now first. The position is
	// checked in the DO UPDATE half (ExportAssignedArg), not "somewhere among
	// the args": both rows exist, so SET is what assigns their order, and
	// _order is bound twice, so argsContain would accept the 2 from VALUES
	// with a broken SET.
	if !argsContain(tx.execArgs[1], "e-2") {
		t.Errorf("first upsert is not for element e-2: %#v", tx.execArgs[1])
	}
	if got, ok := ExportAssignedArg(tx.execs[1], tx.execArgs[1], "_order"); !ok || got != int64(1) {
		t.Errorf("reordered element e-2: DO UPDATE SET _order = %#v (ok=%v), want 1", got, ok)
	}
	if !argsContain(tx.execArgs[3], "e-1") {
		t.Errorf("second upsert is not for element e-1: %#v", tx.execArgs[3])
	}
	if got, ok := ExportAssignedArg(tx.execs[3], tx.execArgs[3], "_order"); !ok || got != int64(2) {
		t.Errorf("reordered element e-1: DO UPDATE SET _order = %#v (ok=%v), want 2", got, ok)
	}
	// The field value also comes from SET, not only from VALUES: for an
	// existing row the insert never runs.
	if got, ok := ExportAssignedArg(tx.execs[1], tx.execArgs[1], "title"); !ok || got != "second" {
		t.Errorf("element e-2: DO UPDATE SET title = %#v (ok=%v), want \"second\"", got, ok)
	}
}

// TestChildRowsSaveEmptyListDeletesEverything: an empty list is legitimate
// input (every element was removed), and the single DELETE must go out without
// NOT IN: with an empty set that is invalid SQL, a 500 on every such form.
func TestChildRowsSaveEmptyListDeletesEverything(t *testing.T) {
	cell := ChildRows(itinTable(), itinDecl())
	tx := &scriptQ{results: [][][]any{{{"mine-1"}}}}

	if _, err := cell.save(context.Background(), tx, "p-1", []any{}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if len(tx.execs) != 1 {
		t.Fatalf("want exactly 1 statement (the delete), got %d: %#v", len(tx.execs), tx.execs)
	}
	if !strings.HasPrefix(strings.TrimSpace(tx.execs[0]), "DELETE") || strings.Contains(tx.execs[0], "NOT IN") {
		t.Errorf("empty list must delete the whole partition without NOT IN:\n%s", tx.execs[0])
	}
	if !argsContain(tx.execArgs[0], "p-1") {
		t.Errorf("delete is not scoped to the parent: %#v", tx.execArgs[0])
	}
}

// TestChildRowsSaveRejectsNonList: not a list means nothing parsed, nothing to
// write. Without that branch a string payload would wipe the whole list with
// one DELETE.
func TestChildRowsSaveRejectsNonList(t *testing.T) {
	cell := ChildRows(itinTable(), itinDecl())
	tx := &scriptQ{}

	got, err := cell.save(context.Background(), tx, "p-1", "not-a-list")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if got != nil {
		t.Errorf("save returned %#v, want nil (nothing was written)", got)
	}
	if len(tx.execs) != 0 {
		t.Errorf("save executed %d statement(s) for a non-list payload, want 0: %#v", len(tx.execs), tx.execs)
	}
}

// TestChildRowsSaveSkipsUnparsedElement: a non-object element is skipped but
// does not consume a position, or positions would run 1,3 instead of
// consecutively (same reason as the separate pos counter in replaceValues).
func TestChildRowsSaveSkipsUnparsedElement(t *testing.T) {
	cell := ChildRows(itinTable(), itinDecl())
	tx := &scriptQ{}

	if _, err := cell.save(context.Background(), tx, "p-1", []any{
		"garbage",
		map[string]any{"title": "real"},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	upserts := execsOf(tx, "ON CONFLICT")
	if len(upserts) != 1 {
		t.Fatalf("want 1 upsert (the garbage element is skipped), got %d: %#v", len(upserts), tx.execs)
	}
	if !argsContain(upserts[0], int64(1)) {
		t.Errorf("the surviving element must take position 1, not the index of its slot: %#v", upserts[0])
	}
	// The garbage element consumes no id either: a fresh NewID() for it would
	// travel into the keep-list as a DELETE parameter, protecting a row that
	// does not exist. The DELETE args are the parent plus exactly one kept id;
	// "what counts as an element" must agree between resolveIDs and writeRows.
	deletes := execsOf(tx, "DELETE")
	if len(deletes) != 1 || len(deletes[0]) != 2 {
		t.Errorf("delete args = %#v, want exactly the parent plus the one real element's id", deletes)
	}
}

// TestChildRowsLoadReadsSatellitesPerElement: the read returns a list of
// objects whose column fields go through their own decodes and whose satellite
// is read with its own query by the element's id.
func TestChildRowsLoadReadsSatellitesPerElement(t *testing.T) {
	cell := ChildRows(itinTable(), itinDeclWithSatellite())
	q := &scriptQ{results: [][][]any{
		{{"e-1", int32(1), "first"}, {"e-2", int32(2), "second"}}, // elements
		{{"en", "one"}}, // satellite of e-1
		{{"en", "two"}}, // satellite of e-2
	}}

	got, err := cell.load(context.Background(), q, "p-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	list, ok := got.([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("load = %#v, want two items", got)
	}
	first, _ := list[0].(map[string]any)
	if first["id"] != "e-1" || first["day"] != float64(1) || first["title"] != "first" {
		t.Errorf("first item = %#v, want id/day/title decoded", first)
	}
	content, _ := first["content"].(map[string]any)
	if content["en"] != "one" {
		t.Errorf("nested satellite of the first item = %#v, want {en: one}", first["content"])
	}
	second, _ := list[1].(map[string]any)
	if sat, _ := second["content"].(map[string]any); sat["en"] != "two" {
		t.Errorf("nested satellite of the second item = %#v, want {en: two}", second["content"])
	}
	// Three queries: one for the elements plus one per element's satellite.
	// N+1 is accepted (see readRows), but "the satellite is read once for all"
	// would be a different bug: every element would get the first one's
	// translations.
	if len(q.queries) != 3 {
		t.Fatalf("queries = %d, want 3 (elements + one satellite per element): %#v", len(q.queries), q.queries)
	}
	if !strings.Contains(q.queries[1], "trips_included_locales") {
		t.Errorf("second query is not the satellite read:\n%s", q.queries[1])
	}
	// Arguments, not only the query text: the satellite is read by the
	// element's id, and substituting the entity's id ("p-1") would hand the
	// form translations belonging to another row, while the double's answers
	// do not depend on the args, so without this check the swap would go
	// unnoticed.
	if !argsContain(q.queryArgs[1], "e-1") || argsContain(q.queryArgs[1], "p-1") {
		t.Errorf("satellite of the first element is read by the wrong parent: %#v", q.queryArgs[1])
	}
	if !argsContain(q.queryArgs[2], "e-2") {
		t.Errorf("satellite of the second element is read by the wrong parent: %#v", q.queryArgs[2])
	}
}

// TestChildRowsLoadEmptyIsArray: an empty list is an array, not nil, or null
// would reach the widget instead of an empty list.
func TestChildRowsLoadEmptyIsArray(t *testing.T) {
	cell := ChildRows(itinTable(), itinDecl())
	got, err := cell.load(context.Background(), &scriptQ{}, "p-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	list, ok := got.([]any)
	if !ok || list == nil {
		t.Fatalf("load = %#v, want []any{}", got)
	}
	if len(list) != 0 {
		t.Fatalf("load = %#v, want empty", got)
	}
}

// TestKeyedChildRowsSavePartitionsIndependently: "absent key means untouched"
// per partition, as in KeyedChildValues. A garbage value leaves its partition
// alone, an empty key is skipped (otherwise the keyless scope would wipe every
// locale at once), and each partition's DELETE carries its own key.
func TestKeyedChildRowsSavePartitionsIndependently(t *testing.T) {
	ct := itinTable()
	ct.Key, ct.KeyType = itKey, "public._locales"
	cell := KeyedChildRows(ct, itinDecl())

	tx := &scriptQ{results: [][][]any{{}, {}}} // ownedIDs is empty for both partitions
	patch := map[string]any{
		"de": []any{map[string]any{"title": "eins"}},
		"en": []any{map[string]any{"title": "one"}},
		"ru": "not-a-list", // garbage: the partition is left alone
		"":   []any{},      // empty key: no partition
	}
	if _, err := cell.save(context.Background(), tx, "p-1", patch); err != nil {
		t.Fatalf("save: %v", err)
	}

	deletes := execsOf(tx, "DELETE")
	if len(deletes) != 2 {
		t.Fatalf("want 2 DELETEs (one per REAL partition), got %d: %#v", len(deletes), tx.execs)
	}
	// sortedMapKeys walks the patch alphabetically: "de" before "en".
	if !argsContain(deletes[0], "de") || argsContain(deletes[0], "en") {
		t.Errorf("first delete (partition \"de\") args = %#v, want to carry \"de\" only", deletes[0])
	}
	if !argsContain(deletes[1], "en") || argsContain(deletes[1], "de") {
		t.Errorf("second delete (partition \"en\") args = %#v, want to carry \"en\" only", deletes[1])
	}
	upserts := execsOf(tx, "ON CONFLICT")
	if len(upserts) != 2 {
		t.Fatalf("want 2 upserts (one element per real partition), got %d: %#v", len(upserts), tx.execs)
	}
}

// TestKeyedChildRowsOwnershipIsPerPartition: an id owned by partition "de" but
// submitted under key "en" is the same forgery as a foreign parent, only within
// one entity. Ownership is per partition (ownedIDs is called with its own key),
// otherwise the upsert under "en" would rewrite the "de" locale's row, keeping
// its _locale, since the key column is not part of DO UPDATE.
//
// The previous test could not see this: it scripted an empty ownedIDs for both
// partitions, so an "own" id could never appear.
func TestKeyedChildRowsOwnershipIsPerPartition(t *testing.T) {
	ct := itinTable()
	ct.Key, ct.KeyType = itKey, "public._locales"
	cell := KeyedChildRows(ct, itinDecl())

	// sortedMapKeys: "de" first (owns shared-1), then "en" (does not).
	tx := &scriptQ{results: [][][]any{{{"shared-1"}}, {}}}
	patch := map[string]any{
		"de": []any{map[string]any{"id": "shared-1", "title": "eins"}},
		"en": []any{map[string]any{"id": "shared-1", "title": "one"}},
	}
	if _, err := cell.save(context.Background(), tx, "p-1", patch); err != nil {
		t.Fatalf("save: %v", err)
	}

	upserts := execsOf(tx, "ON CONFLICT")
	if len(upserts) != 2 {
		t.Fatalf("want 2 upserts (one per partition), got %d: %#v", len(upserts), tx.execs)
	}
	if !argsContain(upserts[0], "shared-1") {
		t.Errorf("partition \"de\" owns shared-1 and must upsert under it: %#v", upserts[0])
	}
	if argsContain(upserts[1], "shared-1") {
		t.Error("an id owned by partition \"de\" reached the upsert of partition \"en\" — it would rewrite the other locale's row")
	}
	if !argsContain(upserts[1], "generated") {
		t.Errorf("partition \"en\" must treat the foreign id as a new element: %#v", upserts[1])
	}
}

// TestKeyedChildRowsLoadRejectsMisalignedRow: the same strictness as rowItem. A
// row with the wrong number of values is an error, not "skip it": a silently
// swallowed element would look absent, and the next save would delete its row
// together with the nested ones.
func TestKeyedChildRowsLoadRejectsMisalignedRow(t *testing.T) {
	ct := itinTable()
	ct.Key, ct.KeyType = itKey, "public._locales"
	cell := KeyedChildRows(ct, itinDecl())

	// The declaration expects the key plus three columns; the row carries two.
	q := &scriptQ{results: [][][]any{{{"en", "e-1"}}}}
	if _, err := cell.load(context.Background(), q, "p-1"); err == nil {
		t.Fatal("a row with the wrong number of values was swallowed instead of raising")
	}
}

// TestKeyedChildRowsLoadSplitsByPartition: the read returns a "key -> list of
// objects" map split by the row's own partition column.
func TestKeyedChildRowsLoadSplitsByPartition(t *testing.T) {
	ct := itinTable()
	ct.Key, ct.KeyType = itKey, "public._locales"
	cell := KeyedChildRows(ct, itinDecl())

	q := &scriptQ{results: [][][]any{{
		{"en", "e-1", int32(1), "first"},
		{"ru", "r-1", int32(1), "первый"},
		{"en", "e-2", int32(2), "second"},
	}}}
	got, err := cell.load(context.Background(), q, "p-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("load = %#v, want a map", got)
	}
	en, _ := m["en"].([]any)
	ru, _ := m["ru"].([]any)
	if len(en) != 2 || len(ru) != 1 {
		t.Fatalf("partitions = en:%d ru:%d, want en:2 ru:1 (%#v)", len(en), len(ru), m)
	}
	if item, _ := en[0].(map[string]any); item["id"] != "e-1" || item["title"] != "first" {
		t.Errorf("first en item = %#v, want the row's own values", en[0])
	}
	if item, _ := ru[0].(map[string]any); item["id"] != "r-1" {
		t.Errorf("ru item = %#v, want the ru row (partitions must not bleed)", ru[0])
	}
	// One query for every partition: the key column is in the projection and
	// each row says where it belongs (see readKeyedValues).
	if len(q.queries) != 1 {
		t.Fatalf("queries = %d, want 1: %#v", len(q.queries), q.queries)
	}
}

// TestChildRowsOwnedIDsScopedToParent: the set of "own" ids is read with the
// same scope as the write, otherwise another parent's id would count as "own"
// and the whole forgery gate would be decoration.
func TestChildRowsOwnedIDsScopedToParent(t *testing.T) {
	ct := itinTable()
	ct.Key, ct.KeyType = itKey, "public._locales"
	q := &scriptQ{results: [][][]any{{{"e-1"}, {nil}}}}

	owned, err := ct.ownedIDs(context.Background(), q, "p-1", "en")
	if err != nil {
		t.Fatalf("ownedIDs: %v", err)
	}
	if !owned["e-1"] || len(owned) != 1 {
		t.Fatalf("owned = %#v, want only e-1 (a NULL id is not an id)", owned)
	}
	if !strings.Contains(q.queries[0], "_locale = $2::text::public._locales") {
		t.Errorf("ownership query is not scoped to the partition:\n%s", q.queries[0])
	}
}

// nonColumnProjection is a postgres.Projection that deliberately does not
// implement postgres.Column: CAST(...) is a plain expression, not a table
// column reference (no Name()/TableName()). It builds a cell with value!=nil
// but a projection unusable in INSERT ... VALUES, a state unreachable through
// the public constructors in column.go (proj and value always come from one
// cell[E]) but expressible by assembling Column{} directly in this package.
func nonColumnProjection() postgres.Projection {
	return postgres.CAST(postgres.String("x")).AS_TEXT()
}

// TestChildValuesPanicsOnNonColumnProjection: the gate on cell.proj. If a cell
// with value but without a column projection slipped into ChildValues, the
// panic would come from the type assertion inside insertValueSQL
// (cell.proj.(postgres.Column)) on the first Save of a live form, not at
// declaration time.
func TestChildValuesPanicsOnNonColumnProjection(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("ChildValues with a non-Column projection did not panic")
		}
	}()
	badCell := Column{
		proj:  nonColumnProjection(),
		wire:  UUID(icValue).wire,
		value: UUID(icValue).value,
	}
	ChildValues(testChildTable(), badCell)
}

// TestKeyedChildValuesPanicsOnNonColumnProjection: the same gate on the map
// variant.
func TestKeyedChildValuesPanicsOnNonColumnProjection(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("KeyedChildValues with a non-Column projection did not panic")
		}
	}()
	ct := testChildTable()
	ct.Key, ct.KeyType = icKey, "public._locales"
	badCell := Column{
		proj:  nonColumnProjection(),
		wire:  UUID(icValue).wire,
		value: UUID(icValue).value,
	}
	KeyedChildValues(ct, badCell)
}
