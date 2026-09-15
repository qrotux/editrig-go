package erjet

import (
	"strings"
	"testing"

	"github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/qrotux/editrig-go/ui"
)

// Hand-written go-jet description instead of generated code: the cell needs
// only a typed column, no live schema behind it.
var (
	todIDCol    = postgres.StringColumn("id")
	todStartCol = postgres.TimeColumn("start_time")
	todTable    = postgres.NewTable("public", "timeofday_rows", "", todIDCol, todStartCol)
)

func TestTimeOfDayDecode(t *testing.T) {
	col := TimeOfDay(todStartCol)
	cases := []struct {
		in   any
		want any
	}{
		{pgtype.Time{Microseconds: (13*3600 + 5*60) * 1_000_000, Valid: true}, "13:05"},
		{pgtype.Time{Valid: false}, nil},
		{pgtype.Time{Microseconds: 0, Valid: true}, "00:00"},
		{"07:30", "07:30"}, // passthrough: a non-pgtype value comes through as is
	}
	for _, c := range cases {
		if got := col.decode(c.in); got != c.want {
			t.Errorf("decode(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestTimeOfDayAcceptsTimeOfDayUI(t *testing.T) {
	col := TimeOfDay(todStartCol)
	if !col.Accepts(ui.TimeOfDay().WireKind()) {
		t.Fatal("TimeOfDay cell must accept ui.TimeOfDay wire")
	}
}

// TestTimeOfDayAssign pins the write: an empty string and nil are SQL NULL,
// garbage is ok=false (the column is silently skipped).
func TestTimeOfDayAssign(t *testing.T) {
	col := TimeOfDay(todStartCol)
	cases := []struct {
		in       any
		wantEcho any
		wantOK   bool
	}{
		{nil, nil, true},
		{"", nil, true},
		{"13:05", "13:05", true},
		{"25:99", nil, false},
		{"nonsense", nil, false},
		{42.0, nil, false},
	}
	for _, c := range cases {
		assign, echo, ok := col.assign(c.in)
		if ok != c.wantOK {
			t.Errorf("assign(%v) ok = %v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if echo != c.wantEcho {
			t.Errorf("assign(%v) echo = %v, want %v", c.in, echo, c.wantEcho)
		}
		if assign == nil {
			t.Errorf("assign(%v) = nil assignment", c.in)
		}
	}
}

// TestTimeOfDayAssignsSQLNull pins that clearing reaches SET as the NULL
// keyword, not a parameter. postgres.TimeT(zero time) gives the same echo and
// ok, so the test above would let such an implementation through and the
// column would get 00:00 - midnight instead of "no time".
func TestTimeOfDayAssignsSQLNull(t *testing.T) {
	tbl := Table{
		Src: todTable, ID: todIDCol,
		Cols: []Named{{Name: "start_time", Column: TimeOfDay(todStartCol)}},
	}
	for _, in := range []any{nil, ""} {
		assigns, _, _ := tbl.Assignments(map[string]any{"start_time": in})
		if len(assigns) != 1 {
			t.Fatalf("assignments for %#v = %d, want 1", in, len(assigns))
		}
		sql, args := tbl.UpdateSQL(assigns, nil, "x")
		if !strings.Contains(sql, "SET start_time = NULL") {
			t.Errorf("UPDATE for %#v = %s, want SET start_time = NULL", in, sql)
		}
		// One argument, the id from WHERE: the time value cannot travel as a
		// parameter, NULL is written into the text.
		if len(args) != 1 {
			t.Errorf("args for %#v = %v, want just the id", in, args)
		}
	}
}
