package erjet

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/go-jet/jet/v2/postgres"
)

var qtyCol = postgres.IntegerColumn("qty")

func TestIntWritesWholeNumbers(t *testing.T) {
	c := Int(qtyCol)
	_, echo, ok := c.assign(float64(42))
	if !ok {
		t.Fatal("assign(42) refused a whole number")
	}
	if fmt.Sprint(echo) != "42" {
		t.Errorf("echo = %#v, want 42", echo)
	}
}

func TestIntWritesNull(t *testing.T) {
	c := Int(qtyCol)
	as, echo, ok := c.assign(nil)
	if !ok || as == nil {
		t.Fatalf("assign(nil): as=%v ok=%v - nil must clear the column", as, ok)
	}
	if echo != nil {
		t.Errorf("echo = %#v, want nil", echo)
	}
}

// A non-integer is not rounded: structural validation of type:integer runs
// earlier and this path is defensive. Rounding would silently write something
// other than what the form showed.
func TestIntRefusesNonIntegral(t *testing.T) {
	c := Int(qtyCol)
	for _, v := range []float64{3.7, math.NaN(), math.Inf(1), math.Inf(-1), 1 << 54} {
		if _, _, ok := c.assign(v); ok {
			t.Errorf("assign(%v) accepted - want refusal", v)
		}
	}
}

func TestIntRefusesNonNumbers(t *testing.T) {
	c := Int(qtyCol)
	if _, _, ok := c.assign("42"); ok {
		t.Error(`assign("42") accepted - a string is not a number on the wire`)
	}
}

// Both sides of the audit diff must be one type: "before" comes through
// Normalize from pgx (int32/int64), "after" from assign. Differing types would
// report a false change on every Save.
func TestIntEchoMatchesNormalize(t *testing.T) {
	c := Int(qtyCol)
	_, echo, ok := c.assign(float64(5))
	if !ok {
		t.Fatal("assign(5) refused")
	}
	for _, fromDB := range []any{int16(5), int32(5), int64(5)} {
		if got := fmt.Sprint(Normalize(fromDB)); got != fmt.Sprint(echo) {
			t.Errorf("Normalize(%T(5)) = %s, echo = %s - diff would report a false change",
				fromDB, got, fmt.Sprint(echo))
		}
	}
}

// JSON -0 decodes to a negative-zero float64: it passes every wholeNumber
// check and is written as int64(0), but without normalization it would echo
// as -0.0, and the fmt.Sprint comparison of the audit diff would report a
// false change on every Save.
func TestIntNegativeZeroEchoesAsZero(t *testing.T) {
	var negZero float64
	if err := json.Unmarshal([]byte("-0"), &negZero); err != nil {
		t.Fatalf("json.Unmarshal(-0): %v", err)
	}
	c := Int(qtyCol)
	_, echo, ok := c.assign(negZero)
	if !ok {
		t.Fatal("assign refused -0")
	}
	if fmt.Sprint(echo) != "0" {
		t.Errorf("echo = %v, want 0 - fmt.Sprint must match what reading the same value returns", echo)
	}
}

var localAtCol = postgres.TimestampColumn("local_at")

// The browser's <input type="datetime-local"> does not return .value
// verbatim: it re-serializes per the HTML spec, and the form sends back both
// non-canonical shapes depending on whether seconds are zero (checked on
// jsdom):
//
//	set "2026-08-05T10:00:00" -> .value returns "2026-08-05T10:00"
//	set "2026-08-05T10:00:05" -> .value returns "2026-08-05T10:00:05.000"
//
// Accepting only the canon, the cell would return ok=false, Assignments would
// silently skip the column, and the form would "save" without writing
// anything - no error, no trace in the journal.
func TestTimeLocalAcceptsAllThreeLayouts(t *testing.T) {
	c := TimeLocal(localAtCol)
	cases := map[string]string{
		"2026-08-05T10:00:00":     "2026-08-05T10:00:00", // canonical
		"2026-08-05T10:00":        "2026-08-05T10:00:00", // no seconds
		"2026-08-05T10:00:05.000": "2026-08-05T10:00:05", // milliseconds
	}
	for in, want := range cases {
		_, echo, ok := c.assign(in)
		if !ok {
			t.Fatalf("assign(%q) refused", in)
		}
		if echo != want {
			t.Errorf("assign(%q) echo = %#v, want canonical %q", in, echo, want)
		}
	}
}

// A zone in the string means the value did not come from the wall-clock
// widget. Silently truncating it would write a different instant than the
// form showed.
func TestTimeLocalRefusesZonedStrings(t *testing.T) {
	c := TimeLocal(localAtCol)
	for _, in := range []string{"2026-01-02T08:04:05+05:00", "2026-01-02T08:04:05Z"} {
		if _, _, ok := c.assign(in); ok {
			t.Errorf("assign(%q) accepted - wall-clock must not carry a zone", in)
		}
	}
}

func TestTimeLocalClears(t *testing.T) {
	c := TimeLocal(localAtCol)
	for _, in := range []any{nil, ""} {
		as, echo, ok := c.assign(in)
		if !ok || as == nil {
			t.Fatalf("assign(%#v): as=%v ok=%v - must clear the column", in, as, ok)
		}
		if echo != nil {
			t.Errorf("assign(%#v) echo = %#v, want nil", in, echo)
		}
	}
}

// Own decode, not Normalize: that one formats in UTC with a Z suffix, which
// lies about the data of a zoneless column.
func TestTimeLocalDecodeHasNoZone(t *testing.T) {
	c := TimeLocal(localAtCol)
	got := c.decode(time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC))
	if got != "2026-08-05T10:00:00" {
		t.Errorf("decode = %#v, want 2026-08-05T10:00:00 (no zone, no Z)", got)
	}
}

// Round-trip: what is written reads back the same.
func TestTimeLocalEchoMatchesDecode(t *testing.T) {
	c := TimeLocal(localAtCol)
	_, echo, ok := c.assign("2026-08-05T10:00")
	if !ok {
		t.Fatal("assign refused")
	}
	fromDB := c.decode(time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC))
	if fmt.Sprint(echo) != fmt.Sprint(fromDB) {
		t.Errorf("echo = %v, decode = %v - diff would report a false change", echo, fromDB)
	}
}

var dueCol = postgres.TimestampzColumn("due_at")

// Round-trip: a string written as a date reads back as the same value, with
// no hours and no zone - decode formats the same "YYYY-MM-DD".
func TestDateEchoMatchesDecode(t *testing.T) {
	c := Date(dueCol)
	_, echo, ok := c.assign("2026-08-05")
	if !ok {
		t.Fatal("assign refused")
	}
	fromDB := c.decode(time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC))
	if fmt.Sprint(echo) != fmt.Sprint(fromDB) {
		t.Errorf("echo = %v, decode = %v - diff would report a false change", echo, fromDB)
	}
}

// The column stores an instant; the cell must put UTC midnight of that day
// there, not the current time or noon.
func TestDateWritesUTCMidnight(t *testing.T) {
	c := Date(dueCol)
	as, _, ok := c.assign("2026-08-05")
	if !ok || as == nil {
		t.Fatalf("assign(2026-08-05): as=%v ok=%v", as, ok)
	}
}

// An empty or absent string clears the column, symmetric with Time.
func TestDateClears(t *testing.T) {
	c := Date(dueCol)
	for _, in := range []any{nil, ""} {
		as, echo, ok := c.assign(in)
		if !ok || as == nil {
			t.Fatalf("assign(%#v): as=%v ok=%v - must clear the column", in, as, ok)
		}
		if echo != nil {
			t.Errorf("assign(%#v) echo = %#v, want nil", in, echo)
		}
	}
}

// An unparseable value never reaches the database: assign returns ok=false
// and Table.Assignments silently skips the column, the same behaviour as Time
// on a format mismatch (see TestTimeLocalRefusesZonedStrings).
func TestDateRefusesMalformed(t *testing.T) {
	c := Date(dueCol)
	for _, in := range []any{"2026-08-05T10:00:00Z", "not-a-date", "05/08/2026", 42.0} {
		if _, _, ok := c.assign(in); ok {
			t.Errorf("assign(%#v) accepted - want refusal", in)
		}
	}
}

// Own decode, not Normalize: that one produces RFC 3339 with a zone, extra
// information a date field must not carry in its editable value.
func TestDateDecodeHasNoTimeOrZone(t *testing.T) {
	c := Date(dueCol)
	got := c.decode(time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC))
	if got != "2026-08-05" {
		t.Errorf("decode = %#v, want 2026-08-05", got)
	}
}
