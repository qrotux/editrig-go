package erjet

import (
	"context"
	"strings"
	"testing"

	"github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5"
)

// Test relations table: imitates go-jet generated code (id, "order",
// parent_id, path plus one column per collection) in exactly the part the
// builders need.
var (
	relID     = postgres.IntegerColumn("id")
	relOrder  = postgres.IntegerColumn("order")
	relParent = postgres.StringColumn("parent_id")
	relPath   = postgres.StringColumn("path")
	relTags   = postgres.StringColumn("tags_id")
	relsTbl   = postgres.NewTable("public", "users_rels", "", relID, relOrder, relParent, relPath, relTags)
)

func testRels() RelsTable {
	return RelsTable{
		Src: relsTbl, ID: relID, Parent: relParent, Path: relPath, Order: relOrder,
		ParentType: "uuid", TargetType: "uuid",
	}
}

// --- read doubles ----------------------------------------------------------------
//
// fakeRows in store_test.go returns exactly one row, whereas a relation list
// is several rows in a given order; hence a multi-row double of its own.

type multiRows struct {
	pgx.Rows
	rows [][]any
	i    int
}

func (r *multiRows) Next() bool {
	if r.i >= len(r.rows) {
		return false
	}
	r.i++
	return true
}
func (r *multiRows) Values() ([]any, error) { return r.rows[r.i-1], nil }
func (r *multiRows) Close()                 {}
func (r *multiRows) Err() error             { return nil }

type recQuerier struct {
	sql  []string
	rows [][]any
}

func (q *recQuerier) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	q.sql = append(q.sql, sql)
	return &multiRows{rows: q.rows}, nil
}

// TestRelsCellIsSatellite pins that a relations cell has no column in the
// main table yet is writable: without both flags at once it would either be
// pulled into the shared SELECT (nil projection, RowSQL panics) or rejected
// by decl.Lint.
func TestRelsCellIsSatellite(t *testing.T) {
	c := Rels(testRels(), "interests", relTags)
	if !c.IsSatellite() {
		t.Error("Rels cell must be a satellite (no projection in the parent table)")
	}
	if !c.Writable() {
		t.Error("Rels cell must be writable (through save)")
	}
}

// TestRelsLoadIsOrdered pins that the relation order is significant (it is
// the "order" column), so the read must set it explicitly: without ORDER BY
// Postgres may return rows in any order, and the form would show the chips
// shuffled from one request to the next.
func TestRelsLoadIsOrdered(t *testing.T) {
	c := Rels(testRels(), "interests", relTags)
	q := &recQuerier{rows: [][]any{{"a"}, {"b"}}}

	got, err := c.load(context.Background(), q, "u1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ids, ok := got.([]any)
	if !ok || len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("load = %#v, want []any{a b}", got)
	}
	if len(q.sql) != 1 {
		t.Fatalf("queries = %d, want 1", len(q.sql))
	}
	sql := q.sql[0]
	if !strings.Contains(strings.ToUpper(sql), "ORDER BY") {
		t.Errorf("load SQL has no ORDER BY:\n%s", sql)
	}
	if !strings.Contains(sql, `"order"`) {
		t.Errorf("load SQL does not order by the order column:\n%s", sql)
	}
	if !strings.Contains(strings.ToUpper(sql), "IS NOT NULL") {
		t.Errorf("load SQL must skip rows of other paths (NULL target):\n%s", sql)
	}
}

// An empty relation is never "absent": the form expects an array, nil would
// reach it as null, and the widget would crash on iteration.
func TestRelsLoadEmptyIsArray(t *testing.T) {
	c := Rels(testRels(), "interests", relTags)
	got, err := c.load(context.Background(), &recQuerier{}, "u1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ids, ok := got.([]any)
	if !ok || ids == nil || len(ids) != 0 {
		t.Fatalf("load = %#v, want empty []any", got)
	}
}

// TestRelsSaveReplacesInOrder pins that writing is a replacement: DELETE by
// (parent, path) and insert by position. The array order is the "order"
// value, so it may be neither lost nor sorted.
func TestRelsSaveReplacesInOrder(t *testing.T) {
	c := Rels(testRels(), "interests", relTags)
	tx := &fakeTx{}

	got, err := c.save(context.Background(), tx, "u1", []any{"b", "a"})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if len(tx.execs) != 3 {
		t.Fatalf("execs = %d (%v), want DELETE + 2 INSERT", len(tx.execs), tx.execs)
	}
	if !strings.Contains(strings.ToUpper(tx.execs[0]), "DELETE") {
		t.Errorf("first statement must be the DELETE:\n%s", tx.execs[0])
	}
	for _, sql := range tx.execs[1:] {
		if !strings.Contains(strings.ToUpper(sql), "INSERT") {
			t.Errorf("expected INSERT, got:\n%s", sql)
		}
		if !strings.Contains(sql, `"order"`) {
			t.Errorf("INSERT must fill the order column:\n%s", sql)
		}
	}
	ids, ok := got.([]any)
	if !ok || len(ids) != 2 || ids[0] != "b" || ids[1] != "a" {
		t.Fatalf("save returned %#v, want []any{b a} for the audit diff", got)
	}
}

// An empty array clears the relation rather than being "nothing to do": the
// DELETE runs, no inserts follow.
func TestRelsSaveEmptyClears(t *testing.T) {
	c := Rels(testRels(), "interests", relTags)
	tx := &fakeTx{}

	got, err := c.save(context.Background(), tx, "u1", []any{})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if len(tx.execs) != 1 || !strings.Contains(strings.ToUpper(tx.execs[0]), "DELETE") {
		t.Fatalf("execs = %v, want a single DELETE", tx.execs)
	}
	if ids, ok := got.([]any); !ok || len(ids) != 0 {
		t.Fatalf("save returned %#v, want empty []any", got)
	}
}

// Not an array means the value did not parse and nothing is written (the
// same decision as ok==false in assign). Without this branch a string payload
// would erase every relation with one DELETE.
func TestRelsSaveIgnoresNonSlice(t *testing.T) {
	c := Rels(testRels(), "interests", relTags)
	tx := &fakeTx{}

	got, err := c.save(context.Background(), tx, "u1", "oops")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if got != nil {
		t.Errorf("save returned %#v, want nil", got)
	}
	if len(tx.execs) != 0 {
		t.Errorf("execs = %v, want none", tx.execs)
	}
}

// The cell removes duplicates and garbage itself: uniqueItems is a schema
// guarantee, and the cell must be self-sufficient (it is also called past
// structural validation, from tests, migrations, an importer).
func TestRelsSaveDedupsAndSkipsNonStrings(t *testing.T) {
	c := Rels(testRels(), "interests", relTags)
	tx := &fakeTx{}

	got, err := c.save(context.Background(), tx, "u1", []any{"a", "a", 42, "", "b"})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	ids, ok := got.([]any)
	if !ok || len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("save returned %#v, want []any{a b}", got)
	}
	if len(tx.execs) != 3 {
		t.Errorf("execs = %d (%v), want DELETE + 2 INSERT", len(tx.execs), tx.execs)
	}
}

// The cast is mandatory: parent_id and target are uuid while the parameter
// travels as text. The error would surface only against a live database, as
// a 500.
func TestRelsSaveCastsUUIDs(t *testing.T) {
	c := Rels(testRels(), "interests", relTags)
	tx := &fakeTx{}

	if _, err := c.save(context.Background(), tx, "u1", []any{"a"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	insert := tx.execs[len(tx.execs)-1]
	// go-jet renders CAST(...) AS uuid in the postfix form `$n::text::uuid`.
	if strings.Count(insert, "::uuid") < 2 {
		t.Errorf("INSERT must cast both the parent id and the target id to uuid:\n%s", insert)
	}
}
