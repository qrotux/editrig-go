package erjet

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/qrotux/editrig-go"
)

// --- driver doubles ------------------------------------------------------------
//
// pgx.Tx and pgx.Rows are wide interfaces of which Save touches four methods,
// so the doubles embed the interface and override only those: an unimplemented
// method nil-panics in the test that touched it, which flags a widened
// contract without a hundred stubs.
//
// The conformance suite checks the same against a live database, but it skips
// without EDITRIG_TEST_DATABASE_URL, and the traps below (empty SET, satellite
// rollback) are 500s in production.

type fakeRows struct {
	pgx.Rows
	vals []any
	done bool
}

func (r *fakeRows) Next() bool {
	if r.done {
		return false
	}
	r.done = true
	return true
}
func (r *fakeRows) Values() ([]any, error) { return r.vals, nil }
func (r *fakeRows) Close()                 {}
func (r *fakeRows) Err() error             { return nil }

type fakeTx struct {
	pgx.Tx
	execs     []string
	execArgs  [][]any // execArgs[i] pairs with execs[i]; for tests that check what is bound, not only the SQL shape (see child_internal_test.go)
	queries   []string
	committed bool
	rolled    bool
	// vals is what each successive Query returns, one row per call.
	vals [][]any
}

func (tx *fakeTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.execs = append(tx.execs, sql)
	tx.execArgs = append(tx.execArgs, args)
	return pgconn.CommandTag{}, nil
}

func (tx *fakeTx) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	tx.queries = append(tx.queries, sql)
	if len(tx.vals) == 0 {
		return &fakeRows{done: true}, nil // no rows
	}
	v := tx.vals[0]
	tx.vals = tx.vals[1:]
	return &fakeRows{vals: v}, nil
}

func (tx *fakeTx) Commit(context.Context) error   { tx.committed = true; return nil }
func (tx *fakeTx) Rollback(context.Context) error { tx.rolled = true; return nil }

type fakeDB struct{ tx *fakeTx }

func (db *fakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return &fakeRows{done: true}, nil
}
func (db *fakeDB) Begin(context.Context) (pgx.Tx, error) { return db.tx, nil }

// satStore builds a table with one satellite over the doubles; the satellite
// records what it is given, or fails with saveErr.
func satStore(t *testing.T, saveErr error, written *any) (Store, *fakeTx, *[]editrig.Change) {
	t.Helper()
	tx := &fakeTx{}
	tt := testTable()
	tt.Cols = append(tt.Cols, Named{Name: "bio", Column: Column{
		load: func(context.Context, Querier, string) (any, error) { return map[string]any{"en": "old"}, nil },
		save: func(_ context.Context, _ pgx.Tx, _ string, v any) (any, error) {
			if saveErr != nil {
				return nil, saveErr
			}
			*written = v
			return v, nil
		},
	}})

	var changes []editrig.Change
	return Store{
		DB:    &fakeDB{tx: tx},
		Table: tt,
		Touch: colTick,
		OnWrite: func(_ context.Context, _ string, c []editrig.Change, _ bool) {
			changes = c
		},
	}, tx, &changes
}

// TestSaveSatelliteOnlyPayloadSkipsUpdate pins that a satellite-only payload
// yields no assignment and no UPDATE: UpdateSQL panics on an empty SET, so this
// is a 500 on every save of the field, not a spare query.
func TestSaveSatelliteOnlyPayloadSkipsUpdate(t *testing.T) {
	var got any
	s, tx, changes := satStore(t, nil, &got)
	tx.vals = [][]any{{true}} // Exists: the parent exists

	id := "u1"
	if _, ferr, err := s.Save(context.Background(), &id, map[string]any{"bio": map[string]any{"en": "new"}}); err != nil || ferr != nil {
		t.Fatalf("Save = ferr %v, err %v", ferr, err)
	}
	for _, sql := range tx.execs {
		if strings.Contains(strings.ToUpper(sql), "UPDATE") {
			t.Errorf("UPDATE issued for a satellite-only payload:\n%s", sql)
		}
	}
	if !tx.committed {
		t.Error("transaction not committed")
	}
	if got == nil {
		t.Error("satellite save was never called")
	}
	if len(*changes) != 1 || (*changes)[0].Field != "bio" {
		t.Errorf("changes = %+v, want one bio change", *changes)
	}
}

// TestSaveSatelliteOnlyPayloadOnMissingRow pins that "nothing to write to the
// columns" is not "no row": without the existence check Save would write the
// satellite for a missing parent and answer 200 instead of 404.
func TestSaveSatelliteOnlyPayloadOnMissingRow(t *testing.T) {
	var got any
	s, tx, _ := satStore(t, nil, &got)
	tx.vals = [][]any{{false}} // Exists: no parent

	id := "nope"
	_, _, err := s.Save(context.Background(), &id, map[string]any{"bio": map[string]any{"en": "new"}})
	if !errors.Is(err, editrig.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if got != nil {
		t.Error("satellite was written for a row that does not exist")
	}
	if tx.committed {
		t.Error("transaction committed despite a missing row")
	}
}

// TestSaveRollsBackOnSatelliteError pins the core's atomicity contract: a
// failed satellite takes the already executed UPDATE of the main table and the
// journal entry down with it.
func TestSaveRollsBackOnSatelliteError(t *testing.T) {
	var got any
	s, tx, changes := satStore(t, errors.New("boom"), &got)
	tx.vals = [][]any{{"old-name", nil}} // before: name + bio (the satellite reads its own)

	id := "u1"
	_, _, err := s.Save(context.Background(), &id, map[string]any{
		"name": "new-name",
		"bio":  map[string]any{"en": "new"},
	})
	if err == nil {
		t.Fatal("Save must return the satellite error")
	}
	if tx.committed {
		t.Error("transaction committed despite a failed satellite write")
	}
	if !tx.rolled {
		t.Error("transaction not rolled back")
	}
	if len(*changes) != 0 {
		t.Errorf("journal got %+v for a failed save", *changes)
	}
}

// TestDiffReportsOnlyRealChanges pins that the diff carries only the fields
// that actually changed.
func TestDiffReportsOnlyRealChanges(t *testing.T) {
	before := map[string]any{"city": "Berlin", "name": "Old", "warning_count": float64(3)}
	after := map[string]any{"city": "Berlin", "name": "New", "warning_count": float64(4)}

	got := diff([]string{"city", "name", "warning_count"}, before, after)
	// The diff is reported in the core type, which is what lets the
	// application keep one journal bridge for every store.
	var _ []editrig.Change = got
	if len(got) != 2 {
		t.Fatalf("diff = %+v, want 2 (name, warning_count)", got)
	}
	// Order follows the given keys, not map iteration: the diff goes to the
	// journal and its rows must not flicker between writes.
	if got[0].Field != "name" || got[0].Old != "Old" || got[0].New != "New" {
		t.Errorf("diff[0] = %+v, want name Old→New", got[0])
	}
	if got[1].Field != "warning_count" {
		t.Errorf("diff[1] = %+v, want warning_count", got[1])
	}
}

// TestDiffComparesWireForms pins that both sides arrive normalized (before via
// Normalize, after via Assignments) and are compared by string form; otherwise
// "...10:00:00.000Z" against "...10:00:00Z" or int against float64 would put a
// false change in the journal on every save.
func TestDiffComparesWireForms(t *testing.T) {
	same := diff([]string{"rating", "seen_at"},
		map[string]any{"rating": float64(5), "seen_at": "2026-01-01T10:00:00Z"},
		map[string]any{"rating": float64(5), "seen_at": "2026-01-01T10:00:00Z"},
	)
	if len(same) != 0 {
		t.Errorf("diff on identical wire values = %+v, want none", same)
	}
	// nil <-> value is a real change in both directions (clearing a nullable field).
	if got := diff([]string{"city"}, map[string]any{"city": "Berlin"}, map[string]any{"city": nil}); len(got) != 1 {
		t.Errorf("clearing a field = %+v, want one change", got)
	}
	if got := diff([]string{"city"}, map[string]any{"city": nil}, map[string]any{"city": "Berlin"}); len(got) != 1 {
		t.Errorf("filling a field = %+v, want one change", got)
	}
}

// TestDiffSkipsKeysOutsideTheList pins that the diff follows the list of
// written keys, not the map contents: a stray key in either map must not reach
// the journal.
func TestDiffSkipsKeysOutsideTheList(t *testing.T) {
	got := diff([]string{"city"},
		map[string]any{"city": "Berlin", "rating": float64(1)},
		map[string]any{"city": "Praha", "rating": float64(9)},
	)
	if len(got) != 1 || got[0].Field != "city" {
		t.Errorf("diff = %+v, want only city", got)
	}
}

// --- deferred phase + ordinary columns on create -------------------------------
//
// A separate table rather than testTable(): go-jet rebinds a column to the last
// table it was passed to (jet.NewTable calls c.setTableName), so reusing
// colID/colName from column_test.go here would break the tests above.
var (
	pColID    = postgres.StringColumn("id")
	pColName  = postgres.StringColumn("name")
	pColPhoto = postgres.StringColumn("photo_id")
	pColTick  = postgres.TimestampzColumn("updated_at")
	pTbl      = postgres.NewTable("public", "profiles", "", pColID, pColName, pColPhoto, pColTick)
)

// deferredTable pairs an ordinary column with a deferred column cell, the
// shape of a media field: photo_id depends on an owner row that does not yet
// exist on create.
func deferredTable(save DeferredFunc) Table {
	return Table{
		Src: pTbl,
		ID:  pColID,
		Cols: []Named{
			{Name: "id", Column: ReadOnly(pColID)},
			{Name: "name", Column: Text(pColName)},
			{Name: "photo_id", Column: Deferred(pColPhoto, save)},
		},
	}
}

// TestDeferredColumnIsWrittenBySecondPhase pins that a deferred column is
// skipped by the first phase (Assignments drops a non-null value) and written
// by the second, which assigns the result to its column in a separate UPDATE.
func TestDeferredColumnIsWrittenBySecondPhase(t *testing.T) {
	var gotParent string
	var gotVal any
	save := func(_ context.Context, _ pgx.Tx, parentID string, v any) (any, error) {
		gotParent, gotVal = parentID, v
		return "media-1", nil
	}
	tx := &fakeTx{vals: [][]any{{nil}}} // before: photo_id (not set yet)
	s := Store{DB: &fakeDB{tx: tx}, Table: deferredTable(save), Touch: pColTick}

	id := "u1"
	if _, ferr, err := s.Save(context.Background(), &id, map[string]any{"photo_id": "upload-7"}); err != nil || ferr != nil {
		t.Fatalf("Save = ferr %v, err %v", ferr, err)
	}
	if gotParent != id {
		t.Errorf("deferred save got parentID %q, want %q", gotParent, id)
	}
	if gotVal != "upload-7" {
		t.Errorf("deferred save got value %v, want the raw wire value upload-7", gotVal)
	}
	var sawUpdate bool
	for _, sql := range tx.execs {
		if strings.Contains(sql, "photo_id") {
			sawUpdate = true
		}
	}
	if !sawUpdate {
		t.Fatalf("no UPDATE assigned photo_id, execs=%v", tx.execs)
	}
	if !tx.committed {
		t.Error("transaction not committed")
	}
}

// TestExplicitNullOnDeferredColumnGoesThroughAssign pins that an explicit null
// clears in the first phase: in the second, save would return nil, Store would
// continue, and the column would never be nulled.
func TestExplicitNullOnDeferredColumnGoesThroughAssign(t *testing.T) {
	var saveCalled bool
	save := func(context.Context, pgx.Tx, string, any) (any, error) {
		saveCalled = true
		return nil, nil
	}
	tx := &fakeTx{vals: [][]any{{"old-media-id"}}} // before: photo_id
	var changes []editrig.Change
	s := Store{
		DB: &fakeDB{tx: tx}, Table: deferredTable(save), Touch: pColTick,
		OnWrite: func(_ context.Context, _ string, c []editrig.Change, _ bool) { changes = c },
	}

	id := "u1"
	if _, ferr, err := s.Save(context.Background(), &id, map[string]any{"photo_id": nil}); err != nil || ferr != nil {
		t.Fatalf("Save = ferr %v, err %v", ferr, err)
	}
	if saveCalled {
		t.Error("explicit null must clear via assign in the first phase — reaching the deferred save would leave the column unchanged")
	}
	if len(tx.execs) != 1 || !strings.Contains(tx.execs[0], "photo_id") {
		t.Fatalf("execs = %v, want exactly one UPDATE touching photo_id", tx.execs)
	}
	if len(changes) != 1 || changes[0].Field != "photo_id" || changes[0].New != nil {
		t.Errorf("changes = %+v, want one photo_id → nil", changes)
	}
}

// TestExplicitNullOnSatelliteGoesThroughSave pins that a satellite (assign ==
// nil) receives an explicit null through save; without the IsSatellite() skip
// Save would dereference a nil func.
func TestExplicitNullOnSatelliteGoesThroughSave(t *testing.T) {
	var called bool
	var gotVal any
	tx := &fakeTx{vals: [][]any{{true}}} // Exists: the parent exists
	tt := testTable()
	tt.Cols = append(tt.Cols, Named{Name: "bio", Column: Column{
		load: func(context.Context, Querier, string) (any, error) { return map[string]any{"en": "old"}, nil },
		save: func(_ context.Context, _ pgx.Tx, _ string, v any) (any, error) {
			called, gotVal = true, v
			return nil, nil
		},
	}})
	s := Store{DB: &fakeDB{tx: tx}, Table: tt, Touch: colTick}

	id := "u1"
	if _, ferr, err := s.Save(context.Background(), &id, map[string]any{"bio": nil}); err != nil || ferr != nil {
		t.Fatalf("Save = ferr %v, err %v", ferr, err)
	}
	if !called {
		t.Fatal("explicit null on a satellite must reach save — a blanket nil-skip would drop {\"bio\":null} on the floor")
	}
	if gotVal != nil {
		t.Errorf("save got %v, want nil (the explicit clear itself)", gotVal)
	}
}

// TestDeferredPhaseRunsOnCreateWithNewID pins that on create the deferred
// phase receives the real id returned by the Create hook, not a placeholder.
func TestDeferredPhaseRunsOnCreateWithNewID(t *testing.T) {
	var gotParent string
	save := func(_ context.Context, _ pgx.Tx, parentID string, v any) (any, error) {
		gotParent = parentID
		return "media-9", nil
	}
	tx := &fakeTx{}
	create := func(_ context.Context, _ pgx.Tx, in map[string]any) (string, []editrig.Change, error) {
		return "new-id-1", nil, nil
	}
	s := Store{DB: &fakeDB{tx: tx}, Table: deferredTable(save), Touch: pColTick, Create: create}

	if _, ferr, err := s.Save(context.Background(), nil, map[string]any{"photo_id": "upload-1"}); err != nil || ferr != nil {
		t.Fatalf("Save = ferr %v, err %v", ferr, err)
	}
	if gotParent != "new-id-1" {
		t.Errorf("deferred save got parentID %q, want the fresh create id %q", gotParent, "new-id-1")
	}
	if !tx.committed {
		t.Error("transaction not committed")
	}
}

// TestOrdinaryColumnsAreWrittenOnCreate pins that on create the ordinary
// columns of the payload reach the row through the second UPDATE; otherwise a
// full create form silently loses everything the hook does not insert.
func TestOrdinaryColumnsAreWrittenOnCreate(t *testing.T) {
	tx := &fakeTx{}
	create := func(_ context.Context, _ pgx.Tx, in map[string]any) (string, []editrig.Change, error) {
		return "new-1", nil, nil
	}
	var changes []editrig.Change
	s := Store{
		DB: &fakeDB{tx: tx}, Table: testTable(), Touch: colTick, Create: create,
		OnWrite: func(_ context.Context, _ string, c []editrig.Change, _ bool) { changes = c },
	}

	if _, ferr, err := s.Save(context.Background(), nil, map[string]any{"name": "widget", "qty": float64(3)}); err != nil || ferr != nil {
		t.Fatalf("Save = ferr %v, err %v", ferr, err)
	}
	var sawBoth bool
	for _, sql := range tx.execs {
		if strings.Contains(sql, "name") && strings.Contains(sql, "qty") {
			sawBoth = true
		}
	}
	if !sawBoth {
		t.Fatalf("no UPDATE carried name/qty from the payload, execs=%v", tx.execs)
	}
	byField := map[string]editrig.Change{}
	for _, c := range changes {
		byField[c.Field] = c
	}
	if c, ok := byField["name"]; !ok || c.New != "widget" || c.Old != nil {
		t.Errorf("changes[name] = %+v, want New=widget Old=nil (no before-read exists on create)", c)
	}
}

// TestCreateDiffDoesNotDuplicateHookFields pins that a field reported by both
// the Create hook and Assignments appears once, with the Assignments value.
func TestCreateDiffDoesNotDuplicateHookFields(t *testing.T) {
	tx := &fakeTx{}
	create := func(_ context.Context, _ pgx.Tx, in map[string]any) (string, []editrig.Change, error) {
		return "new-2", []editrig.Change{{Field: "name", New: "from-hook"}}, nil
	}
	var changes []editrig.Change
	s := Store{
		DB: &fakeDB{tx: tx}, Table: testTable(), Touch: colTick, Create: create,
		OnWrite: func(_ context.Context, _ string, c []editrig.Change, _ bool) { changes = c },
	}

	if _, ferr, err := s.Save(context.Background(), nil, map[string]any{"name": "from-payload"}); err != nil || ferr != nil {
		t.Fatalf("Save = ferr %v, err %v", ferr, err)
	}
	var count int
	var got editrig.Change
	for _, c := range changes {
		if c.Field == "name" {
			count++
			got = c
		}
	}
	if count != 1 {
		t.Fatalf("name reported %d times in %+v, want once", count, changes)
	}
	if got.New != "from-payload" {
		t.Errorf("name = %v, want from-payload (the Assignments value, not the hook's)", got.New)
	}
}

// TestDeferredSaveErrorRollsBackInsertAndUpdate pins the core's atomicity
// contract: a failed deferred phase takes the hook's INSERT and the first
// UPDATE down with it.
func TestDeferredSaveErrorRollsBackInsertAndUpdate(t *testing.T) {
	boom := errors.New("boom")
	save := func(context.Context, pgx.Tx, string, any) (any, error) { return nil, boom }
	tx := &fakeTx{}
	create := func(_ context.Context, _ pgx.Tx, in map[string]any) (string, []editrig.Change, error) {
		return "new-3", nil, nil
	}
	s := Store{DB: &fakeDB{tx: tx}, Table: deferredTable(save), Touch: pColTick, Create: create}

	_, _, err := s.Save(context.Background(), nil, map[string]any{"photo_id": "upload-x", "name": "widget"})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if tx.committed {
		t.Error("transaction committed despite a failed deferred save")
	}
	if !tx.rolled {
		t.Error("transaction not rolled back — defer Rollback must unwind both the INSERT and the first UPDATE")
	}
}

// TestDeferredOnlyPayloadDoesNotBuildEmptyUpdate pins that a save which did
// not parse the value returns nil, assignsBack stays empty and UpdateSQL (which
// panics on an empty SET) is never called.
func TestDeferredOnlyPayloadDoesNotBuildEmptyUpdate(t *testing.T) {
	save := func(context.Context, pgx.Tx, string, any) (any, error) { return nil, nil }
	tx := &fakeTx{vals: [][]any{{nil}}} // before: photo_id
	s := Store{DB: &fakeDB{tx: tx}, Table: deferredTable(save), Touch: pColTick}

	id := "u1"
	if _, ferr, err := s.Save(context.Background(), &id, map[string]any{"photo_id": "unparseable"}); err != nil || ferr != nil {
		t.Fatalf("Save = ferr %v, err %v", ferr, err)
	}
	for _, sql := range tx.execs {
		if strings.Contains(strings.ToUpper(sql), "UPDATE") {
			t.Errorf("UPDATE issued despite an empty assignment set: %s", sql)
		}
	}
	if !tx.committed {
		t.Error("transaction not committed")
	}
}
