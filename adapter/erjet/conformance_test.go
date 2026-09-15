package erjet_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/qrotux/editrig-go"
	"github.com/qrotux/editrig-go/adapter/erjet"
	"github.com/qrotux/editrig-go/decl"
	"github.com/qrotux/editrig-go/storetest"
	"github.com/qrotux/editrig-go/ui"
)

// TestConformance runs erjet against the core's conformance suite.
//
// An external test (package erjet_test) on purpose: the suite must exercise
// the store through exactly the API the application sees. The fixture table is
// created by DDL on an ephemeral database and its go-jet description is written
// by hand (postgres.NewTable/*Column), which also shows erjet does not depend
// on codegen.
func TestConformance(t *testing.T) {
	pool := ephemeralPool(t)
	newSubject := conformanceSubject(pool)

	storetest.Run(t, newSubject)
	// The optional sub-suites erjet supports: FieldLocalized is a KeyedStrings
	// cell over the fixture's side table; the extra types are Int/TimeLocal/
	// Strings/Ints/Floats/Timestamps cells; FieldValues/FieldItems are
	// KeyedChildValues and ChildRows over two child tables, the element of the
	// second carrying its own nested satellite; FieldBlocks is KeyedChildRows
	// over storetest_blocks with ui.Keyed(ui.Items()).
	storetest.RunSatellite(t, newSubject)
	storetest.RunExtraTypes(t, newSubject)
	storetest.RunChildRows(t, newSubject)
	storetest.RunKeyedChildRows(t, newSubject)
}

// TestConformanceFixtureLints runs the fixture declaration through decl.Lint.
//
// The fixture is the only place in the repository where ChildRows and
// KeyedChildValues cells stand next to their ui halves (ui.Items() and
// ui.Keyed(ui.List(...))). The conformance suite calls Load/Save directly,
// bypassing the schema, so a wire-shape mismatch on which a real form saves
// nothing would otherwise pass here silently.
//
// specs is nil: the fixture has no section registry, and the checks about
// sections are legitimately skipped.
func TestConformanceFixtureLints(t *testing.T) {
	_, fixture := fixtureTable()
	if problems := decl.Lint(fixture, nil); len(problems) > 0 {
		t.Errorf("fixture declaration does not lint clean:\n\t%s", strings.Join(problems, "\n\t"))
	}
}

// TestConformanceKeyedListOnAKeyedStringCellIsRejected pins the one pair the
// type axis must reject where the cost of missing it is data loss, not a
// silently unsaved field.
//
// A "localized list of tags" looks plausible: ui.Keyed(ui.List(ui.String()),
// keys) over erjet.KeyedStrings. Both halves are maps over locales, and if the
// inner list collapsed to KindAny, Wire.Match would short-circuit and
// decl.Lint would be clean. On Save the keyed table parses each key's value
// as a string, []any yields "", and "" there means clear: every submitted
// locale would lose its translation, committed, without a single error.
//
// No database is needed; the gate is declarative.
func TestConformanceKeyedListOnAKeyedStringCellIsRejected(t *testing.T) {
	bad := decl.Set[erjet.Column]{
		{Name: storetest.FieldLocalized,
			Col: erjet.KeyedStrings(sideTable(), satValueCol, erjet.ClearSetNull()),
			UI:  ui.Keyed(ui.List(ui.String()), ui.Keys("en", "ru", "fr"))},
	}
	problems := decl.Lint(bad, nil)
	for _, p := range problems {
		if strings.Contains(p, storetest.FieldLocalized) && strings.Contains(p, "does not accept") {
			return
		}
	}
	t.Errorf("Lint() = %v — a map-of-lists field over a map-of-strings cell must be rejected: "+
		"the cell parses each key as a string, a list yields \"\", and \"\" means CLEAR — every submitted "+
		"locale would lose its translation", problems)
}

// TestConformanceKeyedChildValuesAgreesWithoutWildcard is the other side of the
// same gate: the fixture's legal pair must pass by agreement, not by wildcard.
// Wire.Match short-circuits on KindAny from either side, so green means
// nothing while one half is KindAny, and the mirror-image mismatch (a
// map-of-lists cell under a map-of-strings field) would pass with it. Both
// shapes are compared literally.
func TestConformanceKeyedChildValuesAgreesWithoutWildcard(t *testing.T) {
	want := ui.Wire{Kind: ui.KindMap, Elem: ui.KindList}

	declared := ui.Keyed(ui.List(ui.String()), ui.Keys("en", "ru")).WireKind()
	if declared != want {
		t.Errorf("ui half declares %+v, want %+v", declared, want)
	}
	cell := erjet.ExportWire(erjet.KeyedChildValues(valuesChild(), erjet.Str(valuesValueCol, "")))
	if cell != want {
		t.Errorf("storage half declares %+v, want %+v", cell, want)
	}
	if declared.Elem == ui.KindAny || cell.Elem == ui.KindAny {
		t.Errorf("one half declares no element kind (%+v vs %+v) — the pair would pass by wildcard, "+
			"and the mirror-image mismatch would pass with it", declared, cell)
	}
}

// TestConformanceChildOrderIsOneBased reads the child-table position from the
// database and pins that positions are numbered from one within each partition
// separately.
//
// erjet-local rather than part of storetest: _order is row bookkeeping, not a
// form field, and never surfaces through storetest.Subject. The unit tests in
// child_test.go pin 1-based on the argument of the built query; here the check
// is that this value actually landed in the row and was not overwritten on the
// way (DEFAULT, a trigger, a second UPDATE).
//
// 0-based positions sort just as well, and so does numbering across all
// partitions (en:1,2 / ru:3,4): every per-locale list still sorts correctly
// and no RunChildRows check notices. The drift only shows next to data where
// each partition is numbered 1..N from the start.
func TestConformanceChildOrderIsOneBased(t *testing.T) {
	pool := ephemeralPool(t)
	s := conformanceSubject(pool)(t)
	ctx := context.Background()

	id, _, err := s.Save(ctx, nil, map[string]any{storetest.FieldTitle: "t"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := s.Save(ctx, &id, map[string]any{
		storetest.FieldItems: []any{
			map[string]any{storetest.ItemFieldQty: 1.0},
			map[string]any{storetest.ItemFieldQty: 2.0},
		},
		// Two partitions, not one: on one, global numbering is indistinguishable
		// from per-partition numbering.
		storetest.FieldValues: map[string]any{
			"en": []any{"a", "b"},
			"ru": []any{"c", "d"},
		},
	}); err != nil {
		t.Fatalf("write both blocks: %v", err)
	}

	// Non-partitioned table: one group under the key "".
	assertOrders(t, pool, "storetest_items", "''", id, map[string][]int32{"": {1, 2}})
	// Partitioned: numbering starts from one in each locale.
	assertOrders(t, pool, "storetest_values", "locale::text", id, map[string][]int32{
		"en": {1, 2},
		"ru": {1, 2},
	})
}

// assertOrders compares the child-table positions grouped by partition.
// keyExpr is the partition expression (an empty string literal for a
// non-partitioned table with exactly one group): a test SQL literal, not data.
func assertOrders(t *testing.T, pool *pgxpool.Pool, table, keyExpr, parentID string, want map[string][]int32) {
	t.Helper()
	sql := "SELECT " + keyExpr + ", _order FROM " + table +
		" WHERE _parent_id::text = $1 ORDER BY 1, _order"
	rows, err := pool.Query(context.Background(), sql, parentID)
	if err != nil {
		t.Fatalf("%s: %v", table, err)
	}
	got := map[string][]int32{}
	for rows.Next() {
		var key string
		var pos int32
		if err := rows.Scan(&key, &pos); err != nil {
			t.Fatalf("%s scan: %v", table, err)
		}
		got[key] = append(got[key], pos)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("%s rows: %v", table, err)
	}

	if len(got) != len(want) {
		t.Fatalf("%s has %d partitions, want %d: %v", table, len(got), len(want), got)
	}
	for key, wantPos := range want {
		gotPos := got[key]
		if len(gotPos) != len(wantPos) {
			t.Errorf("%s[%q]._order = %v, want %v", table, key, gotPos, wantPos)
			continue
		}
		for i := range wantPos {
			if gotPos[i] != wantPos[i] {
				t.Errorf("%s[%q]._order = %v, want %v — positions are numbered from one WITHIN each partition",
					table, key, gotPos, wantPos)
				break
			}
		}
	}
}

func conformanceSubject(pool *pgxpool.Pool) storetest.New {
	tbl, fixture := fixtureTable()
	table := erjet.NewTable(fixture, tbl, idCol)

	return func(t *testing.T) storetest.Subject {
		ctx := context.Background()
		// A clean store for every check: the suite expects no state to leak
		// between them. Translations go by FK cascade.
		if _, err := pool.Exec(ctx, "TRUNCATE TABLE storetest_rows CASCADE"); err != nil {
			t.Fatalf("truncate: %v", err)
		}

		var written []editrig.Change
		store := erjet.Store{
			DB:      pool,
			Table:   table,
			Create:  createRow(tbl),
			OnWrite: func(_ context.Context, _ string, ch []editrig.Change, _ bool) { written = ch },
		}
		return storetest.Subject{
			Load:    store.Load,
			Save:    store.Save,
			Delete:  store.Delete,
			Written: func() []editrig.Change { return written },
			SetRaw: func(ctx context.Context, id, field string, value any) error {
				// The column name comes from a suite constant, not from data.
				sql := fmt.Sprintf("UPDATE storetest_rows SET %s = $1 WHERE id = $2::uuid", field)
				_, err := pool.Exec(ctx, sql, value, id)
				return err
			},
		}
	}
}

// TestClearPolicyProtectsSiblingColumn pins what the clear policy exists for:
// two localized columns live in one side-table row, and clearing the first
// must not take the second away. Any second localized field on the same side
// table produces this configuration.
func TestClearPolicyProtectsSiblingColumn(t *testing.T) {
	pool := ephemeralPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "TRUNCATE TABLE storetest_rows CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	store := twoSatelliteStore(pool)
	id, _, err := store.Save(ctx, nil, map[string]any{storetest.FieldTitle: "t"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := store.Save(ctx, &id, map[string]any{
		storetest.FieldLocalized: map[string]any{"en": "bio"},
		fieldOther:               map[string]any{"en": "tagline"},
	}); err != nil {
		t.Fatalf("fill both: %v", err)
	}

	// Clear the first field through ClearSetNull.
	if _, _, err := store.Save(ctx, &id, map[string]any{
		storetest.FieldLocalized: map[string]any{"en": ""},
	}); err != nil {
		t.Fatalf("clear first: %v", err)
	}

	row, err := store.Load(ctx, id)
	if err != nil || row == nil {
		t.Fatalf("load: err=%v row=%v", err, row)
	}
	if got := row[storetest.FieldLocalized].(map[string]any); len(got) != 0 {
		t.Errorf("%s = %v, want cleared", storetest.FieldLocalized, got)
	}
	if got := row[fieldOther].(map[string]any); got["en"] != "tagline" {
		t.Errorf("%s = %v — clearing a sibling column must not touch it", fieldOther, got)
	}
}

// TestClearDeleteRowRemovesTheWholeRow pins the opposite policy and its price:
// the row goes as a whole, sibling column included, as ClearDeleteRow
// documents.
func TestClearDeleteRowRemovesTheWholeRow(t *testing.T) {
	pool := ephemeralPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "TRUNCATE TABLE storetest_rows CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	store := twoSatelliteStore(pool)
	// Same table, but the FieldLocalized cell deletes the row instead of
	// nulling the column. Found by name, not by index: twoSatelliteStore
	// prepends fieldOther, so Cols[0] is fieldOther, and replacing it would put
	// two different cells on one physical column (satValueCol) inside one
	// upsert, which Postgres rejects ("column specified more than once").
	for i, c := range store.Table.Cols {
		if c.Name == storetest.FieldLocalized {
			store.Table.Cols[i].Column = erjet.KeyedStrings(sideTable(), satValueCol, erjet.ClearDeleteRow())
		}
	}

	id, _, err := store.Save(ctx, nil, map[string]any{storetest.FieldTitle: "t"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := store.Save(ctx, &id, map[string]any{
		storetest.FieldLocalized: map[string]any{"en": "bio"},
		fieldOther:               map[string]any{"en": "tagline"},
	}); err != nil {
		t.Fatalf("fill both: %v", err)
	}
	if _, _, err := store.Save(ctx, &id, map[string]any{
		storetest.FieldLocalized: map[string]any{"en": ""},
	}); err != nil {
		t.Fatalf("clear first: %v", err)
	}

	row, err := store.Load(ctx, id)
	if err != nil || row == nil {
		t.Fatalf("load: err=%v row=%v", err, row)
	}
	if got := row[fieldOther].(map[string]any); len(got) != 0 {
		t.Errorf("%s = %v — ClearDeleteRow is documented to take the whole row", fieldOther, got)
	}
}

// TestSatelliteGroupWriteCreatesRowWithNotNullSibling: a side table with a
// NOT NULL sibling (title NOT NULL, note nullable), no row for this key yet,
// and both fields written in one Save, exactly as a real form sends its PATCH
// (rjsf submits the whole formData, not a diff).
//
// Without the grouped write each cell would write its column with its own
// INSERT ... ON CONFLICT: title creates the row in the first statement, and
// the note statement carries no title at all. Postgres checks NOT NULL on the
// tentative row before resolving ON CONFLICT, so the note statement fails with
// 23502 on every first save of a locale even though title is in the same Save.
func TestSatelliteGroupWriteCreatesRowWithNotNullSibling(t *testing.T) {
	pool := ephemeralPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "TRUNCATE TABLE storetest_rows CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	store := requiredSatelliteStore(pool)
	id, _, err := store.Save(ctx, nil, map[string]any{storetest.FieldTitle: "t"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, _, err := store.Save(ctx, &id, map[string]any{
		fieldReqTitle: map[string]any{"ru": "Заголовок"},
		fieldReqNote:  map[string]any{"ru": "Заметка"},
	}); err != nil {
		t.Fatalf("save title+note for a new locale row: %v", err)
	}

	row, err := store.Load(ctx, id)
	if err != nil || row == nil {
		t.Fatalf("load: err=%v row=%v", err, row)
	}
	if got := row[fieldReqTitle].(map[string]any)["ru"]; got != "Заголовок" {
		t.Errorf("%s[ru] = %v, want Заголовок", fieldReqTitle, got)
	}
	if got := row[fieldReqNote].(map[string]any)["ru"]; got != "Заметка" {
		t.Errorf("%s[ru] = %v, want Заметка", fieldReqNote, got)
	}
}

// TestSatelliteGroupWriteUpdatesNotNullSiblingRow: the same shape, but the
// locale row already exists and the second Save edits only note while
// resending title unchanged, as a real form does. Without grouping the note
// cell would issue one INSERT with its own column, title would not be in it,
// and ON CONFLICT would fail on a row that already carries title.
func TestSatelliteGroupWriteUpdatesNotNullSiblingRow(t *testing.T) {
	pool := ephemeralPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "TRUNCATE TABLE storetest_rows CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	store := requiredSatelliteStore(pool)
	id, _, err := store.Save(ctx, nil, map[string]any{storetest.FieldTitle: "t"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// First Save: title only, the locale row appears with title and no note.
	if _, _, err := store.Save(ctx, &id, map[string]any{
		fieldReqTitle: map[string]any{"ru": "Исходный"},
	}); err != nil {
		t.Fatalf("seed title-only row: %v", err)
	}

	// Second Save: the form resends title unchanged and adds note.
	if _, _, err := store.Save(ctx, &id, map[string]any{
		fieldReqTitle: map[string]any{"ru": "Исходный"},
		fieldReqNote:  map[string]any{"ru": "Добавлено"},
	}); err != nil {
		t.Fatalf("update note on an existing NOT NULL-sibling row: %v", err)
	}

	row, err := store.Load(ctx, id)
	if err != nil || row == nil {
		t.Fatalf("load: err=%v row=%v", err, row)
	}
	if got := row[fieldReqTitle].(map[string]any)["ru"]; got != "Исходный" {
		t.Errorf("%s[ru] = %v, want Исходный (untouched)", fieldReqTitle, got)
	}
	if got := row[fieldReqNote].(map[string]any)["ru"]; got != "Добавлено" {
		t.Errorf("%s[ru] = %v, want Добавлено", fieldReqNote, got)
	}
}

// TestSatelliteGroupWriteClearPolicyStillApplies pins that grouping changes
// only the path of non-empty values; a clear stays a separate UPDATE on its
// own column, and both policies (ClearSetNull on note, ClearSet("") on the NOT
// NULL title) behave as they do for a single cell.
func TestSatelliteGroupWriteClearPolicyStillApplies(t *testing.T) {
	pool := ephemeralPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "TRUNCATE TABLE storetest_rows CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	store := requiredSatelliteStore(pool)
	id, _, err := store.Save(ctx, nil, map[string]any{storetest.FieldTitle: "t"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := store.Save(ctx, &id, map[string]any{
		fieldReqTitle: map[string]any{"ru": "Заголовок"},
		fieldReqNote:  map[string]any{"ru": "Заметка"},
	}); err != nil {
		t.Fatalf("seed both: %v", err)
	}

	// ClearSetNull: note (nullable) is nulled, title is resent unchanged, the
	// same request shape as a real form.
	if _, _, err := store.Save(ctx, &id, map[string]any{
		fieldReqTitle: map[string]any{"ru": "Заголовок"},
		fieldReqNote:  map[string]any{"ru": ""},
	}); err != nil {
		t.Fatalf("clear note: %v", err)
	}
	row, err := store.Load(ctx, id)
	if err != nil || row == nil {
		t.Fatalf("load after clearing note: err=%v row=%v", err, row)
	}
	if got, ok := row[fieldReqNote].(map[string]any)["ru"]; ok {
		t.Errorf("%s[ru] = %v, want cleared (absent)", fieldReqNote, got)
	}
	if got := row[fieldReqTitle].(map[string]any)["ru"]; got != "Заголовок" {
		t.Errorf("%s[ru] = %v, want Заголовок (untouched by note's clear)", fieldReqTitle, got)
	}

	// ClearSet(""): the NOT NULL title cannot become NULL, so a clear writes ""
	// and the row is not deleted.
	if _, _, err := store.Save(ctx, &id, map[string]any{
		fieldReqTitle: map[string]any{"ru": ""},
	}); err != nil {
		t.Fatalf("clear title via ClearSet: %v", err)
	}
	var title string
	var titleIsNull bool
	if err := pool.QueryRow(ctx,
		"SELECT title, title IS NULL FROM storetest_required_locales WHERE parent_id = $1 AND locale = 'ru'", id,
	).Scan(&title, &titleIsNull); err != nil {
		t.Fatalf("read title row directly: %v", err)
	}
	if titleIsNull || title != "" {
		t.Errorf("title = %q (null=%v), want empty string per ClearSet(\"\")", title, titleIsNull)
	}
}

// fieldReqTitle/fieldReqNote are the fields over storetest_required_locales.
const (
	fieldReqTitle = "req_title"
	fieldReqNote  = "req_note"
)

var (
	reqParentCol = postgres.StringColumn("parent_id")
	reqKeyCol    = postgres.StringColumn("locale")
	reqTitleCol  = postgres.StringColumn("title")
	reqNoteCol   = postgres.StringColumn("note")
)

// requiredSideTable is a side table with a NOT NULL sibling (title NOT NULL
// without DEFAULT, note nullable). A separate physical table rather than a
// third column in sideTable(): that one is shared by the other conformance
// tests, and a NOT NULL column without DEFAULT would break every insert of
// theirs, since none of their cells writes it.
func requiredSideTable() erjet.KeyedTable {
	return erjet.KeyedTable{
		Src: postgres.NewTable("public", "storetest_required_locales", "",
			reqParentCol, reqKeyCol, reqTitleCol, reqNoteCol),
		Parent:     reqParentCol,
		Key:        reqKeyCol,
		ParentType: "uuid",
		KeyType:    "storetest_locale",
	}
}

// requiredSatelliteStore builds a store with two KeyedStrings cells over
// requiredSideTable(): one NOT NULL (ClearSet, cannot be nulled), one nullable
// (ClearSetNull).
func requiredSatelliteStore(pool *pgxpool.Pool) erjet.Store {
	tbl, _ := fixtureTable()
	side := requiredSideTable()
	fixture := decl.Set[erjet.Column]{
		{Name: storetest.FieldTitle, Col: erjet.Str(titleCol, ""), UI: ui.String().Required()},
		{Name: fieldReqTitle, Col: erjet.KeyedStrings(side, reqTitleCol, erjet.ClearSet(postgres.String(""))),
			UI: ui.Keyed(ui.String(), ui.Keys("en", "ru", "fr"))},
		{Name: fieldReqNote, Col: erjet.KeyedStrings(side, reqNoteCol, erjet.ClearSetNull()),
			UI: ui.Keyed(ui.String(), ui.Keys("en", "ru", "fr"))},
	}

	return erjet.Store{
		DB:     pool,
		Table:  erjet.NewTable(fixture, tbl, idCol),
		Create: createRow(tbl),
	}
}

// fieldOther is the second localized field; the storetest suite knows only
// its own FieldLocalized.
const fieldOther = "other_localized"

// twoSatelliteStore builds a store with two cells over one side table.
func twoSatelliteStore(pool *pgxpool.Pool) erjet.Store {
	tbl, fixture := fixtureTable()
	side := sideTable()
	fixture = append(decl.Set[erjet.Column]{
		// ui.Keyed rather than ui.String(), as for FieldLocalized: the cell
		// accepts a map, and a string declaration would fail the type axis
		// (decl.Lint).
		{Name: fieldOther, Col: erjet.KeyedStrings(side, satOtherCol, erjet.ClearSetNull()),
			UI: ui.Keyed(ui.String(), ui.Keys("en", "ru", "fr"))},
	}, fixture...)

	return erjet.Store{
		DB:     pool,
		Table:  erjet.NewTable(fixture, tbl, idCol),
		Create: createRow(tbl),
	}
}

// --- fixture ------------------------------------------------------------------

// sideTable is the fixture's side table, one function because several cells
// sit over it and the description must not diverge between them.
func sideTable() erjet.KeyedTable {
	return erjet.KeyedTable{
		Src: postgres.NewTable("public", "storetest_locales", "",
			satParentCol, satKeyCol, satValueCol, satOtherCol),
		Parent:     satParentCol,
		Key:        satKeyCol,
		ParentType: "uuid",
		KeyType:    "storetest_locale",
	}
}

var (
	idCol      = postgres.StringColumn(storetest.FieldID)
	titleCol   = postgres.StringColumn(storetest.FieldTitle)
	noteCol    = postgres.StringColumn(storetest.FieldNote)
	counterCol = postgres.FloatColumn(storetest.FieldCounter)
	touchedCol = postgres.TimestampzColumn(storetest.FieldTouched)
	roCol      = postgres.StringColumn(storetest.FieldRO)

	qtyCol      = postgres.IntegerColumn(storetest.FieldQty)
	localAtCol  = postgres.TimestampColumn(storetest.FieldLocalAt)
	keywordsCol = postgres.StringArrayColumn(storetest.FieldKeywords)
	numsCol     = postgres.IntegerArrayColumn(storetest.FieldNums)
	ratesCol    = postgres.FloatArrayColumn(storetest.FieldRates)
	stampsCol   = postgres.TimestampzArrayColumn(storetest.FieldStamps)

	// Side-table columns. Names of their own: a satellite is tied to the
	// declaration field, not to the column name.
	satParentCol = postgres.StringColumn("parent_id")
	satKeyCol    = postgres.StringColumn("locale")
	satValueCol  = postgres.StringColumn("value")
	// A second localized column in the same row: because of it clearing the
	// first cannot delete the row (see erjet.ClearPolicy).
	satOtherCol = postgres.StringColumn("other")

	// Child-table columns. Each is its own value even when the name repeats
	// ("id", "_parent_id", "locale"): postgres.NewTable stamps the table name
	// onto the column, and one value given to two tables would carry the first
	// table's name into the second; the SQL would build and the filter would
	// reference the wrong table.
	valuesIDCol     = postgres.StringColumn("id")
	valuesParentCol = postgres.StringColumn("_parent_id")
	valuesOrderCol  = postgres.IntegerColumn("_order")
	valuesKeyCol    = postgres.StringColumn("locale")
	valuesValueCol  = postgres.StringColumn("value")

	itemIDCol     = postgres.StringColumn("id")
	itemParentCol = postgres.StringColumn("_parent_id")
	itemOrderCol  = postgres.IntegerColumn("_order")
	itemQtyCol    = postgres.FloatColumn("qty")
	itemTagsCol   = postgres.StringArrayColumn("tags")
	itemMetaCol   = postgres.StringColumn("meta")

	itemLocParentCol = postgres.StringColumn("_parent_id")
	itemLocKeyCol    = postgres.StringColumn("locale")
	itemLocTextCol   = postgres.StringColumn("text")

	// Child-table columns of the keyed object block; own values rather than the
	// item* columns for the same reason as above.
	blockIDCol     = postgres.StringColumn("id")
	blockParentCol = postgres.StringColumn("_parent_id")
	blockOrderCol  = postgres.IntegerColumn("_order")
	blockKeyCol    = postgres.StringColumn("locale")
	blockTitleCol  = postgres.StringColumn("title")
	blockTagsCol   = postgres.StringArrayColumn("tags")
)

// newChildID generates a child-row id: 12 random bytes in hex, 24 characters,
// and the column is declared varchar for it. Local on purpose: ChildTable.NewID
// is a hook rather than a built-in generator, and the fixture shows that the
// caller declares the link between the schema and the generator.
func newChildID() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic("storetest fixture: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buf[:])
}

// valuesChild is the partitioned child table of the scalar block.
func valuesChild() erjet.ChildTable {
	return erjet.ChildTable{
		Src: postgres.NewTable("public", "storetest_values", "",
			valuesOrderCol, valuesParentCol, valuesKeyCol, valuesIDCol, valuesValueCol),
		ID:         valuesIDCol,
		Parent:     valuesParentCol,
		Order:      valuesOrderCol,
		Key:        valuesKeyCol,
		ParentType: "uuid",
		KeyType:    "storetest_locale",
		NewID:      newChildID,
	}
}

// itemsChild is the non-partitioned child table of the object block.
func itemsChild() erjet.ChildTable {
	return erjet.ChildTable{
		Src: postgres.NewTable("public", "storetest_items", "",
			itemOrderCol, itemParentCol, itemIDCol, itemQtyCol, itemTagsCol, itemMetaCol),
		ID:         itemIDCol,
		Parent:     itemParentCol,
		Order:      itemOrderCol,
		ParentType: "uuid",
		NewID:      newChildID,
	}
}

// itemLocalesTable is the element's translation side table. The parent is a
// varchar (the storetest_items row id), so ParentType is empty: the parameter
// travels as text and needs no cast. KeyType is mandatory, the key is an enum.
func itemLocalesTable() erjet.KeyedTable {
	return erjet.KeyedTable{
		Src: postgres.NewTable("public", "storetest_item_locales", "",
			itemLocParentCol, itemLocKeyCol, itemLocTextCol),
		Parent:  itemLocParentCol,
		Key:     itemLocKeyCol,
		KeyType: "storetest_locale",
	}
}

// itemDecl declares the element of the object block with the same decl.Set an
// entity uses. The translation clears with ClearSet(""), not ClearSetNull: the
// text column is NOT NULL and nulling would fail on a live database.
// ClearDeleteRow would take the whole row with its sibling columns.
func itemDecl() decl.Set[erjet.Column] {
	return decl.Set[erjet.Column]{
		{Name: storetest.ItemFieldID, Col: erjet.ReadOnly(itemIDCol),
			UI: ui.String().Hidden().Readonly()},
		{Name: storetest.ItemFieldQty, Col: erjet.Num(itemQtyCol),
			UI: ui.Number().Required()},
		{Name: storetest.ItemFieldText,
			Col: erjet.KeyedStrings(itemLocalesTable(), itemLocTextCol, erjet.ClearSet(postgres.String(""))),
			UI:  ui.Keyed(ui.String(), ui.Keys("en", "ru"))},
		{Name: storetest.ItemFieldTags, Col: erjet.Strings(itemTagsCol), UI: ui.List(ui.String()).Nullable()},
		{Name: storetest.ItemFieldMeta, Col: erjet.JSONEditable(itemMetaCol), UI: ui.JSON()},
	}
}

// blocksChild is the partitioned child table of the object block (object
// element plus a locale in the row itself).
func blocksChild() erjet.ChildTable {
	return erjet.ChildTable{
		Src: postgres.NewTable("public", "storetest_blocks", "",
			blockOrderCol, blockParentCol, blockKeyCol, blockIDCol, blockTitleCol, blockTagsCol),
		ID:         blockIDCol,
		Parent:     blockParentCol,
		Order:      blockOrderCol,
		Key:        blockKeyCol,
		ParentType: "uuid",
		KeyType:    "storetest_locale",
		NewID:      newChildID,
	}
}

// blockDecl declares the element of the keyed block; the array column inside
// the element is deliberate, both halves of its write are exercised only here.
func blockDecl() decl.Set[erjet.Column] {
	return decl.Set[erjet.Column]{
		{Name: storetest.BlockFieldID, Col: erjet.ReadOnly(blockIDCol), UI: ui.String().Hidden().Readonly()},
		{Name: storetest.BlockFieldTitle, Col: erjet.Str(blockTitleCol, ""), UI: ui.String().Required()},
		{Name: storetest.BlockFieldTags, Col: erjet.Strings(blockTagsCol), UI: ui.List(ui.String()).Nullable()},
	}
}

// fixtureTable returns the go-jet description of the fixture table and the
// declaration over it. The cells are the ones a real entity uses: id/ro have no
// assign (ReadOnly makes the write physically unreachable), the rest follow
// the column type.
func fixtureTable() (postgres.Table, decl.Set[erjet.Column]) {
	tbl := postgres.NewTable("public", "storetest_rows", "",
		idCol, titleCol, noteCol, counterCol, touchedCol, roCol,
		qtyCol, localAtCol, keywordsCol, numsCol, ratesCol, stampsCol)

	// The side table with a map field keeps production-like types: an enum key
	// and a uuid parent. Without explicit parameter casts Postgres receives
	// text and fails ("column is of type ... but expression is of type text"),
	// the one mechanism defect a unit test cannot catch.
	side := sideTable()

	return tbl, decl.Set[erjet.Column]{
		// A clear nulls the column rather than deleting the row: a second
		// localized column (satOtherCol) lives in the same row.
		//
		// The ui half is ui.Keyed, not ui.String(): the satellite cell accepts a
		// "key -> string" map (ui.Wire{KindMap, KindString}). The conformance
		// suite bypasses the schema, so a string declaration would break
		// nothing here, while on a real form it means a text input over a map
		// and a Save that "succeeds" without writing the field;
		// TestConformanceFixtureLints guards it.
		{Name: storetest.FieldLocalized, Col: erjet.KeyedStrings(side, satValueCol, erjet.ClearSetNull()),
			UI: ui.Keyed(ui.String(), ui.Keys("en", "ru", "fr"))},
		{Name: storetest.FieldID, Col: erjet.ReadOnly(idCol), UI: ui.String().Hidden().Readonly()},
		{Name: storetest.FieldTitle, Col: erjet.Str(titleCol, ""), UI: ui.String().Required()},
		{Name: storetest.FieldNote, Col: erjet.Text(noteCol), UI: ui.String().Nullable()},
		{Name: storetest.FieldCounter, Col: erjet.Num(counterCol), UI: ui.Number().Nullable()},
		{Name: storetest.FieldTouched, Col: erjet.Time(touchedCol), UI: ui.Timestamp().Nullable().Widget("datetime")},
		{Name: storetest.FieldRO, Col: erjet.ReadOnly(roCol), UI: ui.String().Nullable().Readonly()},

		// Extra types. The fixture is the only place they meet a live database,
		// and the driver's encoding of an array parameter is beyond a unit test.
		{Name: storetest.FieldQty, Col: erjet.Int(qtyCol), UI: ui.Integer().Nullable()},
		{Name: storetest.FieldLocalAt, Col: erjet.TimeLocal(localAtCol), UI: ui.LocalTimestamp().Nullable()},
		{Name: storetest.FieldKeywords, Col: erjet.Strings(keywordsCol), UI: ui.List(ui.String()).Nullable()},
		{Name: storetest.FieldNums, Col: erjet.Ints(numsCol), UI: ui.List(ui.Integer()).Nullable()},
		{Name: storetest.FieldRates, Col: erjet.Floats(ratesCol), UI: ui.List(ui.Number()).Nullable()},
		{Name: storetest.FieldStamps, Col: erjet.Timestamps(stampsCol), UI: ui.List(ui.Timestamp().Widget("datetime")).Nullable()},

		// Repeatable blocks, both axes at once: a scalar element in a
		// partitioned table (one list per locale) and an object element in a
		// non-partitioned one with nested rows of its own. The behaviour the
		// object projection upserts for (element translations keyed by its id)
		// needs the cascade of a live database.
		{Name: storetest.FieldValues, Col: erjet.KeyedChildValues(valuesChild(), erjet.Str(valuesValueCol, "")),
			UI: ui.Keyed(ui.List(ui.String()), ui.Keys("en", "ru"))},
		{Name: storetest.FieldItems, Col: erjet.ChildRows(itemsChild(), itemDecl()),
			Items: itemDecl(), UI: ui.Items()},

		// Partitioned table plus object element; the ui half is Keyed(Items()),
		// which TestConformanceFixtureLints guards end to end.
		{Name: storetest.FieldBlocks, Col: erjet.KeyedChildRows(blocksChild(), blockDecl()),
			Items: blockDecl(), UI: ui.Keyed(ui.Items(), ui.Keys("en", "ru"))},
	}
}

// createRow is the insert hook; by the fixture contract create fills only
// title.
func createRow(tbl postgres.Table) func(context.Context, pgx.Tx, map[string]any) (string, []editrig.Change, error) {
	return func(ctx context.Context, tx pgx.Tx, in map[string]any) (string, []editrig.Change, error) {
		title, _ := in[storetest.FieldTitle].(string)

		sql, args := tbl.INSERT(titleCol).VALUES(title).RETURNING(idCol).Sql()
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return "", nil, err
		}
		defer rows.Close()
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return "", nil, err
			}
			return "", nil, fmt.Errorf("storetest fixture: INSERT returned no id")
		}
		vals, err := rows.Values()
		if err != nil {
			return "", nil, err
		}
		rows.Close()
		id, _ := erjet.Normalize(vals[0]).(string)

		return id, []editrig.Change{{Field: storetest.FieldTitle, New: title}}, nil
	}
}

// --- connection ---------------------------------------------------------------

const fixtureDDL = `
CREATE TABLE IF NOT EXISTS storetest_rows (
	id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	title   text NOT NULL,
	note    text,
	counter numeric,
	touched timestamptz,
	ro      text
);

-- The locale is a separate enum type so the parameter cast is exercised on the
-- shape that requires it.
DO $$ BEGIN
	CREATE TYPE storetest_locale AS ENUM ('en', 'ru', 'fr');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS storetest_locales (
	id        serial PRIMARY KEY,
	parent_id uuid NOT NULL REFERENCES storetest_rows(id) ON DELETE CASCADE,
	locale    storetest_locale NOT NULL,
	value     text,
	other     text
);

-- ON CONFLICT target: the (locale, parent) pair is unique.
CREATE UNIQUE INDEX IF NOT EXISTS storetest_locales_locale_parent
	ON storetest_locales (locale, parent_id);

-- ALTER rather than columns in the CREATE TABLE above: the table is created
-- with IF NOT EXISTS, and on a database that already has it new columns would
-- never appear; the run would fail with "column ... does not exist".
ALTER TABLE storetest_rows ADD COLUMN IF NOT EXISTS qty      integer;
ALTER TABLE storetest_rows ADD COLUMN IF NOT EXISTS local_at timestamp;
ALTER TABLE storetest_rows ADD COLUMN IF NOT EXISTS keywords text[];
ALTER TABLE storetest_rows ADD COLUMN IF NOT EXISTS nums     int4[];
ALTER TABLE storetest_rows ADD COLUMN IF NOT EXISTS rates    float8[];
ALTER TABLE storetest_rows ADD COLUMN IF NOT EXISTS stamps   timestamptz[];

-- Child table of the scalar repeatable block, partitioned: the locale is in
-- the row itself, so the lists of different locales are independent rows with
-- their own length and positions.
CREATE TABLE IF NOT EXISTS storetest_values (
	_order     integer NOT NULL,
	_parent_id uuid NOT NULL REFERENCES storetest_rows(id) ON DELETE CASCADE,
	locale     storetest_locale,
	id         varchar NOT NULL PRIMARY KEY,
	value      text NOT NULL
);

-- Child table of the object repeatable block. id is a varchar without DEFAULT:
-- the application generates it (ChildTable.NewID), not the database, and the
-- translation table below references it.
CREATE TABLE IF NOT EXISTS storetest_items (
	_order     integer NOT NULL,
	_parent_id uuid NOT NULL REFERENCES storetest_rows(id) ON DELETE CASCADE,
	id         varchar NOT NULL PRIMARY KEY,
	qty        numeric NOT NULL
);

-- ALTER rather than columns in the CREATE TABLE above, for the same reason as
-- storetest_rows.
ALTER TABLE storetest_items ADD COLUMN IF NOT EXISTS tags text[];
ALTER TABLE storetest_items ADD COLUMN IF NOT EXISTS meta jsonb;

-- Translation side table of the element: the parent is the varchar id of a
-- storetest_items row, and this CASCADE is exactly why the object projection
-- must preserve row identity.
CREATE TABLE IF NOT EXISTS storetest_item_locales (
	id         serial PRIMARY KEY,
	_parent_id varchar NOT NULL REFERENCES storetest_items(id) ON DELETE CASCADE,
	locale     storetest_locale NOT NULL,
	text       varchar NOT NULL
);

-- ON CONFLICT target of the nested satellite: the (locale, element) pair is
-- unique.
CREATE UNIQUE INDEX IF NOT EXISTS storetest_item_locales_locale_parent
	ON storetest_item_locales (locale, _parent_id);

-- Side table with a NOT NULL sibling (title NOT NULL without DEFAULT, note
-- nullable), separate from storetest_locales: that table is shared through
-- sideTable(), and a NOT NULL column without DEFAULT there would break the
-- inserts of the other conformance tests, which do not write it.
CREATE TABLE IF NOT EXISTS storetest_required_locales (
	id        serial PRIMARY KEY,
	parent_id uuid NOT NULL REFERENCES storetest_rows(id) ON DELETE CASCADE,
	locale    storetest_locale NOT NULL,
	title     text NOT NULL,
	note      text
);

CREATE UNIQUE INDEX IF NOT EXISTS storetest_required_locales_locale_parent
	ON storetest_required_locales (locale, parent_id);

-- Child table of the keyed object block: the locale in the row itself with an
-- object element. tags is an array inside the element; its INSERT and DO
-- UPDATE paths are exercised only here.
CREATE TABLE IF NOT EXISTS storetest_blocks (
	_order     integer NOT NULL,
	_parent_id uuid NOT NULL REFERENCES storetest_rows(id) ON DELETE CASCADE,
	locale     storetest_locale,
	id         varchar NOT NULL PRIMARY KEY,
	title      text NOT NULL,
	tags       text[]
)`

// ephemeralPool opens EDITRIG_TEST_DATABASE_URL and creates the fixture
// tables; without the variable the test skips. DDL is allowed only on a host
// that looks ephemeral: the test creates and truncates tables, so pointing it
// at a development database by mistake must not be possible.
func ephemeralPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("EDITRIG_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("EDITRIG_TEST_DATABASE_URL is not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("EDITRIG_TEST_DATABASE_URL is not a URL: %v", err)
	}
	host := strings.ToLower(u.Hostname())
	ephemeral := host == "localhost" || host == "127.0.0.1" || host == "::1"
	for _, s := range []string{"testdb", "throwaway", "ephemeral"} {
		ephemeral = ephemeral || strings.Contains(host, s)
	}
	if !ephemeral {
		t.Skipf("host %q does not look ephemeral — refusing to run DDL against it", host)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, fixtureDDL); err != nil {
		t.Fatalf("fixture DDL: %v", err)
	}
	return pool
}
