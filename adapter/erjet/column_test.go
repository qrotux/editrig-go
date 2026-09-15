package erjet

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5"

	"github.com/qrotux/editrig-go/ui"
)

// Test table: imitates go-jet generated code in exactly the part the builders
// need, so no generated package is pulled in.
var (
	colID   = postgres.StringColumn("id")
	colName = postgres.StringColumn("name")
	colQty  = postgres.FloatColumn("qty")
	colSeen = postgres.TimestampzColumn("seen_at")
	colTick = postgres.TimestampzColumn("updated_at")
	tbl     = postgres.NewTable("public", "widgets", "", colID, colName, colQty, colSeen, colTick)
)

func testTable() Table {
	return Table{
		Src: tbl,
		ID:  colID,
		Cols: []Named{
			{Name: "id", Column: ReadOnly(colID)},
			{Name: "name", Column: Text(colName)},
			{Name: "qty", Column: Num(colQty)},
			{Name: "seen_at", Column: ReadOnly(colSeen)},
		},
	}
}

// TestOrderFollowsDeclaration pins that order comes from the declaration and
// is declared nowhere else: ui:order, SELECT projections and SET order all
// derive from it.
func TestOrderFollowsDeclaration(t *testing.T) {
	got := testTable().Order()
	want := []string{"id", "name", "qty", "seen_at"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Order() = %v, want %v", got, want)
	}
}

// satellite is a cell whose value lives in another table: no projection here
// and no assignment in this table's SET. Only the shape matters for these
// tests.
func satellite() Column {
	return Column{
		load: func(context.Context, Querier, string) (any, error) { return nil, nil },
		save: func(context.Context, pgx.Tx, string, any) (any, error) { return nil, nil },
	}
}

// TestSatelliteCellIsWritable pins that a satellite accepts writes through
// save, not assign. decl.Lint reads the flag: if Writable() ignored the second
// half, lint would reject the field as "cell not writable, field not marked
// read-only", and a satellite could not be declared at all.
func TestSatelliteCellIsWritable(t *testing.T) {
	sat := satellite()
	if !sat.Writable() {
		t.Error("satellite cell reported as not writable - decl.Lint would reject the field")
	}
	if !sat.IsSatellite() {
		t.Error("cell without a projection is not recognised as a satellite")
	}

	// Ordinary cells never become satellites: SELECT and SET keep building
	// from them directly.
	if Text(colName).IsSatellite() || ReadOnly(colSeen).IsSatellite() {
		t.Error("a column-backed cell must not be reported as a satellite")
	}
	// Read-only stays read-only: neither assign nor save.
	if ReadOnly(colSeen).Writable() {
		t.Error("read-only cell reported as writable")
	}
}

func TestColumnLookup(t *testing.T) {
	tt := testTable()
	if c, ok := tt.Column("name"); !ok || !c.Writable() {
		t.Errorf("name: ok=%v writable=%v, want true/true", ok, c.Writable())
	}
	if c, ok := tt.Column("seen_at"); !ok || c.Writable() {
		t.Errorf("seen_at: ok=%v writable=%v, want true/false", ok, c.Writable())
	}
	if _, ok := tt.Column("nope"); ok {
		t.Error("unknown field reported as declared")
	}
}

// TestAssignmentsIntersectPayload pins the essence of a partial UPDATE: only
// the keys that arrived get an assignment, and only those whose cell is
// writable.
func TestAssignmentsIntersectPayload(t *testing.T) {
	tt := testTable()
	assigns, changed, newVals := tt.Assignments(map[string]any{
		"name":    "widget",
		"seen_at": "2030-01-01T00:00:00Z", // read-only, must not pass
		"nope":    1,                      // not declared at all
	})
	if len(assigns) != 1 || len(changed) != 1 || changed[0] != "name" {
		t.Fatalf("changed = %v (%d assignments), want [name]", changed, len(assigns))
	}
	if newVals["name"] != "widget" {
		t.Errorf("newVals[name] = %v", newVals["name"])
	}

	// Assignment order is the declared one, not the payload's map iteration
	// order: SQL stability and, through it, the statement cache depend on it.
	_, changed, _ = tt.Assignments(map[string]any{"qty": float64(2), "name": "w"})
	if strings.Join(changed, ",") != "name,qty" {
		t.Errorf("changed = %v, want declaration order [name qty]", changed)
	}
}

// TestUpdateSQLIsPartialAndTouches pins that SET holds only the passed
// assignments plus touch = NOW() (row bookkeeping, not from the payload).
// WHERE compares via ::text so the query tolerates garbage in the path
// parameter and returns "no rows" (404) instead of failing on the uuid cast
// (500).
func TestUpdateSQLIsPartialAndTouches(t *testing.T) {
	tt := testTable()
	assigns, _, _ := tt.Assignments(map[string]any{"name": "widget"})

	sql, args := tt.UpdateSQL(assigns, colTick, "abc")
	if !strings.Contains(sql, "name") || !strings.Contains(sql, "updated_at") || !strings.Contains(sql, "NOW()") {
		t.Errorf("UPDATE missing name/updated_at/NOW():\n%s", sql)
	}
	if strings.Contains(sql, "qty") || strings.Contains(sql, "seen_at") {
		t.Errorf("UPDATE touches columns that were not sent:\n%s", sql)
	}
	if !strings.Contains(sql, "::text") {
		t.Errorf("WHERE must compare via ::text (garbage id => 404, not 500):\n%s", sql)
	}
	if len(args) != 2 { // the name value + id
		t.Errorf("args = %v, want 2", args)
	}

	// Without a touch column no extra assignment appears.
	sql, _ = tt.UpdateSQL(assigns, nil, "abc")
	if strings.Contains(sql, "updated_at") {
		t.Errorf("nil touch must not write updated_at:\n%s", sql)
	}
}

// TestUpdateSQLPanicsOnEmpty pins that `UPDATE ... SET WHERE` is syntactically
// invalid and the caller must return early; silently building broken SQL
// would surface the error at runtime on the Postgres side.
func TestUpdateSQLPanicsOnEmpty(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected a panic on an empty assignment set")
		}
	}()
	testTable().UpdateSQL(nil, colTick, "abc")
}

// satelliteTable is the same table plus one satellite field, which can reach
// neither the SELECT nor the SET of this table.
func satelliteTable() Table {
	tt := testTable()
	tt.Cols = append(tt.Cols, Named{Name: "bio", Column: satellite()})
	return tt
}

// TestSplitSeparatesSatellites pins that keys are split in declared order:
// SELECT projections and the order of satellite queries both derive from it.
func TestSplitSeparatesSatellites(t *testing.T) {
	cols, sats := satelliteTable().split([]string{"bio", "name", "qty"})
	if strings.Join(cols, ",") != "name,qty" {
		t.Errorf("cols = %v, want [name qty] in declaration order", cols)
	}
	if strings.Join(sats, ",") != "bio" {
		t.Errorf("sats = %v, want [bio]", sats)
	}
}

// TestRowSQLRejectsSatelliteKeys pins that a satellite in the shared SELECT
// panics: it has no projection and the builder would dereference nil. Same
// decision as UpdateSQL on an empty SET - a silently broken query would fail
// on the Postgres side instead. The normal path is Row, which calls split
// first.
func TestRowSQLRejectsSatelliteKeys(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected a panic on a satellite key in RowSQL")
		}
	}()
	satelliteTable().RowSQL([]string{"name", "bio"}, "abc")
}

// TestAssignmentsSkipSatellites pins that a satellite is writable but has no
// assign: if Assignments did not skip it, the nil function call would crash
// every Save whose payload carries the field.
func TestAssignmentsSkipSatellites(t *testing.T) {
	assigns, changed, _ := satelliteTable().Assignments(map[string]any{
		"bio":  map[string]any{"en": "hi"},
		"name": "widget",
	})
	if len(assigns) != 1 || strings.Join(changed, ",") != "name" {
		t.Errorf("changed = %v (%d assignments), want only [name]", changed, len(assigns))
	}
}

// TestDeferredIntersectsPayload pins the projection paired with Assignments:
// only the deferred keys that arrived, in declared order. A satellite always
// has save != nil (Writable needs at least one half), so the payload
// intersection is enough here; the IsSatellite predicate is covered by a
// column-backed deferred cell in store_test.go.
func TestDeferredIntersectsPayload(t *testing.T) {
	tt := satelliteTable()

	got := tt.Deferred(map[string]any{"bio": map[string]any{"en": "hi"}, "name": "widget"})
	if len(got) != 1 || got[0].Name != "bio" {
		t.Fatalf("Deferred = %+v, want [bio]", got)
	}
	// A key absent from the payload yields no query: "not sent" means "do not
	// touch", exactly as for columns.
	if got := tt.Deferred(map[string]any{"name": "widget"}); len(got) != 0 {
		t.Errorf("Deferred = %+v, want none", got)
	}
}

// TestRowSQLProjectsRequestedKeysOnly pins that the audit reads a subset of
// columns rather than the whole row, so the projection must follow the
// requested keys.
func TestRowSQLProjectsRequestedKeysOnly(t *testing.T) {
	sql, args := testTable().RowSQL([]string{"name", "qty"}, "abc")
	if !strings.Contains(sql, "name") || !strings.Contains(sql, "qty") {
		t.Errorf("SELECT missing requested columns:\n%s", sql)
	}
	if strings.Contains(sql, "seen_at") {
		t.Errorf("SELECT projects a column that was not requested:\n%s", sql)
	}
	if len(args) != 1 {
		t.Errorf("args = %v, want 1 (id)", args)
	}
}

// TestValueAndAssignAgree pins that value and assign accept and reject exactly
// the same inputs and return the same echo. Diverging, they would give a value
// written by UPDATE and silently skipped by the INSERT of a child row, and
// ok=false looks the same on both paths.
//
// Through ExportAssign/ExportValue rather than the fields directly: the hatches
// expose the cell as decl.Cell sees it, and the test must check agreement
// through that API, not through what is visible only from inside.
func TestValueAndAssignAgree(t *testing.T) {
	cases := []struct {
		name string
		col  Column
		in   []any
	}{
		{"Text", Text(postgres.StringColumn("note")), []any{"x", "", nil, 1.0, true}},
		{"Str", Str(postgres.StringColumn("title"), "fallback"), []any{"x", "", nil}},
		{"UUID", UUID(postgres.StringColumn("owner")), []any{"0b1e…", "", nil, 2.0}},
		{"Int", Int(postgres.IntegerColumn("qty")), []any{3.0, 3.5, nil, "3"}},
		{"Num", Num(postgres.FloatColumn("rate")), []any{1.5, nil, "1.5"}},
		{"Bool", Bool(postgres.BoolColumn("flag")), []any{true, false, nil}},
		{"Time", Time(postgres.TimestampzColumn("at")), []any{"2026-08-06T10:00:00Z", "nope", nil}},
		{"TimeLocal", TimeLocal(postgres.TimestampColumn("at")), []any{"2026-08-06T10:00", "nope", nil}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, in := range c.in {
				_, wantEcho, wantOK := ExportAssign(c.col, in)
				_, gotEcho, gotOK := ExportValue(c.col, in)
				if gotOK != wantOK {
					t.Errorf("value(%#v) ok = %v, assign ok = %v", in, gotOK, wantOK)
					continue
				}
				if fmt.Sprint(gotEcho) != fmt.Sprint(wantEcho) {
					t.Errorf("value(%#v) echo = %v, assign echo = %v", in, gotEcho, wantEcho)
				}
			}
		})
	}
}

// TestCellDeclaresWire pins, per constructor, that every cell declares the
// wire shape it can actually parse. A transposition (say Ints with KindNumber
// instead of KindInteger) would otherwise surface only as a red decl.Lint
// gate that looks like a gate bug rather than a typo here.
func TestCellDeclaresWire(t *testing.T) {
	kt := KeyedTable{
		Src: postgres.NewTable("public", "widgets_locales", "",
			colID, postgres.StringColumn("locale"), postgres.StringColumn("bio")),
		Parent: colID,
		Key:    postgres.StringColumn("locale"),
	}
	deferredSave := func(context.Context, pgx.Tx, string, any) (any, error) { return nil, nil }

	cases := []struct {
		name string
		col  Column
		want ui.Wire
	}{
		{"Text", Text(postgres.StringColumn("note")), ui.Wire{Kind: ui.KindString}},
		{"Str", Str(postgres.StringColumn("title"), ""), ui.Wire{Kind: ui.KindString}},
		{"UUID", UUID(postgres.StringColumn("owner")), ui.Wire{Kind: ui.KindString}},
		{"Bool", Bool(postgres.BoolColumn("flag")), ui.Wire{Kind: ui.KindBool}},
		{"Num", Num(postgres.FloatColumn("rate")), ui.Wire{Kind: ui.KindNumber}},
		{"Int", Int(postgres.IntegerColumn("qty")), ui.Wire{Kind: ui.KindInteger}},
		{"Time", Time(postgres.TimestampzColumn("at")), ui.Wire{Kind: ui.KindString}},
		{"TimeLocal", TimeLocal(postgres.TimestampColumn("at")), ui.Wire{Kind: ui.KindString}},
		{"Strings", Strings(keywordsCol), ui.Wire{Kind: ui.KindList, Elem: ui.KindString}},
		{"Ints", Ints(numsCol), ui.Wire{Kind: ui.KindList, Elem: ui.KindInteger}},
		{"Floats", Floats(ratesCol), ui.Wire{Kind: ui.KindList, Elem: ui.KindNumber}},
		{"Timestamps", Timestamps(stampsCol), ui.Wire{Kind: ui.KindList, Elem: ui.KindString}},
		{"Rels", Rels(testRels(), "tags", relTags), ui.Wire{Kind: ui.KindList, Elem: ui.KindString}},
		{"KeyedStrings", KeyedStrings(kt, postgres.StringColumn("bio"), ClearSetNull()), ui.Wire{Kind: ui.KindMap, Elem: ui.KindString}},
		{"ReadOnly", ReadOnly(colSeen), ui.Wire{Kind: ui.KindAny}},
		{"JSON", JSON(colSeen), ui.Wire{Kind: ui.KindAny}},
		{"JSONEditable", JSONEditable(postgres.StringColumn("doc")), ui.Wire{Kind: ui.KindAny}},
		{"Deferred", Deferred(postgres.StringColumn("photo_id"), deferredSave), ui.Wire{Kind: ui.KindAny}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExportWire(c.col); got != c.want {
				t.Errorf("wire = %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestNoValueOnDeliberatelyReadonlyCells pins that ReadOnly/JSON/Deferred
// deliberately expose no value: they do not belong inside a child table row.
// Deferred is the dangerous one: give it a value "for uniformity" and a child
// row could insert the deferred value (a media id) before the parent row
// exists - exactly what Deferred exists to prevent (see its doc comment).
func TestNoValueOnDeliberatelyReadonlyCells(t *testing.T) {
	deferredSave := func(context.Context, pgx.Tx, string, any) (any, error) { return "materialized", nil }

	cases := []struct {
		name       string
		col        Column
		in         any
		wantAssign bool // ok expected from assign(in); value must be false always
	}{
		{"ReadOnly", ReadOnly(colSeen), nil, false},
		{"JSON", JSON(colSeen), nil, false},
		// Deferred: explicit null is an ordinary first-phase assignment, so
		// assign(nil) must be ok=true; a non-empty value never reaches it
		// (Table.Assignments diverts it earlier). value must reject both.
		{"Deferred", Deferred(postgres.StringColumn("photo_id"), deferredSave), nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, ok := ExportValue(c.col, c.in); ok {
				t.Errorf("value(%#v) ok = true, want false - this cell must not be insertable into a child row", c.in)
			}
			if _, _, ok := ExportAssign(c.col, c.in); ok != c.wantAssign {
				t.Errorf("assign(%#v) ok = %v, want %v", c.in, ok, c.wantAssign)
			}
		})
	}
}

// TestJSONEditableValueMatchesAssign pins that a jsonb document is inserted
// and assigned by one expression (CAST ... AS jsonb) and that both halves
// reject an unmarshalable input alike.
func TestJSONEditableValueMatchesAssign(t *testing.T) {
	cell := JSONEditable(postgres.StringColumn("target"))
	for _, in := range []any{map[string]any{"a": 1.0}, nil} {
		expr, _, okV := ExportValue(cell, in)
		_, _, okA := ExportAssign(cell, in)
		if !okV || !okA || expr == nil {
			t.Errorf("in=%#v: value ok=%v assign ok=%v expr nil=%v", in, okV, okA, expr == nil)
		}
	}
	if _, _, ok := ExportValue(cell, func() {}); ok {
		t.Error("value accepted an unmarshalable input")
	}
	if _, _, ok := ExportAssign(cell, func() {}); ok {
		t.Error("assign accepted an unmarshalable input")
	}
}
