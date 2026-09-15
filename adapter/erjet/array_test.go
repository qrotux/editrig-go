package erjet

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/go-jet/jet/v2/postgres"
)

var (
	keywordsCol = postgres.StringArrayColumn("keywords")
	numsCol     = postgres.IntegerArrayColumn("nums")
	ratesCol    = postgres.FloatArrayColumn("rates")
	stampsCol   = postgres.TimestampzArrayColumn("stamps")
)

func TestArrayStringsRoundTrip(t *testing.T) {
	c := Strings(keywordsCol)
	_, echo, ok := c.assign([]any{"b", "a", "b"})
	if !ok {
		t.Fatal("assign refused a list of strings")
	}
	// Order is significant and duplicates survive: array elements are values,
	// not relation ids (Rels deduplicates for a reason; here it would lose
	// data).
	if fmt.Sprint(echo) != "[b a b]" {
		t.Errorf("echo = %v, want [b a b]", echo)
	}
}

// An empty array and NULL are different states and the cell must tell them
// apart: otherwise a nullable column could never be set back to NULL through
// the editor.
func TestArrayEmptyIsNotNull(t *testing.T) {
	c := Strings(keywordsCol)
	asEmpty, echoEmpty, ok := c.assign([]any{})
	if !ok || asEmpty == nil {
		t.Fatalf("assign([]): as=%v ok=%v", asEmpty, ok)
	}
	if fmt.Sprint(echoEmpty) != "[]" {
		t.Errorf("echo for [] = %v, want []", echoEmpty)
	}
	asNull, echoNull, ok := c.assign(nil)
	if !ok || asNull == nil {
		t.Fatalf("assign(nil): as=%v ok=%v", asNull, ok)
	}
	if echoNull != nil {
		t.Errorf("echo for null = %#v, want nil", echoNull)
	}
}

// A partial array is not allowed: writing only the parsed elements would
// silently lose the rest.
func TestArrayRefusesMixedElements(t *testing.T) {
	c := Strings(keywordsCol)
	if _, _, ok := c.assign([]any{"a", 42}); ok {
		t.Error("assign accepted a list with a non-string element")
	}
}

func TestArrayRefusesNonSlice(t *testing.T) {
	c := Strings(keywordsCol)
	if _, _, ok := c.assign("a,b"); ok {
		t.Error("assign accepted a string - a scalar is not a list")
	}
}

func TestArrayIntsRejectNonIntegral(t *testing.T) {
	c := Ints(numsCol)
	if _, _, ok := c.assign([]any{float64(1), 2.5}); ok {
		t.Error("assign accepted a non-integral element")
	}
	_, echo, ok := c.assign([]any{float64(1), float64(2)})
	if !ok {
		t.Fatal("assign refused whole numbers")
	}
	if fmt.Sprint(echo) != "[1 2]" {
		t.Errorf("echo = %v, want [1 2]", echo)
	}
}

// JSON -0 decodes to a negative-zero float64: it passes every wholeNumber
// check (math.Trunc(-0.0) == -0.0, within maxSafeInt) and is written as
// int64(0), but without normalization it would echo as -0.0, and
// fmt.Sprint("[-0]") against fmt.Sprint("[0]") is a false change in the audit
// log on every Save of such a row.
func TestArrayIntsNegativeZeroEchoesAsZero(t *testing.T) {
	var negZero float64
	if err := json.Unmarshal([]byte("-0"), &negZero); err != nil {
		t.Fatalf("json.Unmarshal(-0): %v", err)
	}
	c := Ints(numsCol)
	_, echo, ok := c.assign([]any{negZero})
	if !ok {
		t.Fatal("assign refused -0")
	}
	if fmt.Sprint(echo) != "[0]" {
		t.Errorf("echo = %v, want [0] - fmt.Sprint must match what reading the same value returns", echo)
	}
}

func TestArrayFloatsRoundTrip(t *testing.T) {
	c := Floats(ratesCol)
	_, echo, ok := c.assign([]any{1.5, 2.25})
	if !ok {
		t.Fatal("assign refused floats")
	}
	if fmt.Sprint(echo) != "[1.5 2.25]" {
		t.Errorf("echo = %v, want [1.5 2.25]", echo)
	}
}

// The costliest case: an input with an offset and a read of the same instant
// must give fmt.Sprint-identical values, or the audit diff reports a change on
// every Save - exactly the defect the second result of assign exists for.
func TestArrayTimestampsEchoIsNormalized(t *testing.T) {
	c := Timestamps(stampsCol)
	_, echo, ok := c.assign([]any{"2026-01-02T08:04:05+05:00"})
	if !ok {
		t.Fatal("assign refused an RFC3339 element")
	}
	fromDB := c.decode([]any{time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)})
	if fmt.Sprint(echo) != fmt.Sprint(fromDB) {
		t.Errorf("echo = %v, decode = %v - diff would report a false change on every Save",
			echo, fromDB)
	}
}

// pgx decodes any Postgres array into []any (ArrayCodec.DecodeValue), each
// element already decoded by its element codec: int4[] arrives as
// []any{int32, int32, ...}, never as []int32. decode must run such a boxed
// element through Normalize.
func TestArrayDecodeNormalizesElements(t *testing.T) {
	c := Ints(numsCol)
	got, ok := c.decode([]any{int32(1), int32(2)}).([]any)
	if !ok {
		t.Fatalf("decode = %#v, want []any", c.decode([]any{int32(1), int32(2)}))
	}
	if fmt.Sprint(got) != "[1 2]" {
		t.Errorf("decode = %v, want [1 2]", got)
	}
}

// Read-path coverage for the other element types: pgx always returns []any
// with decoded elements (string, float64), never a typed slice.
func TestArrayDecodeNormalizesStringAndFloatElements(t *testing.T) {
	sc := Strings(keywordsCol)
	if got := fmt.Sprint(sc.decode([]any{"a", "b"})); got != "[a b]" {
		t.Errorf("Strings decode = %v, want [a b]", got)
	}
	fc := Floats(ratesCol)
	if got := fmt.Sprint(fc.decode([]any{1.5, 2.25})); got != "[1.5 2.25]" {
		t.Errorf("Floats decode = %v, want [1.5 2.25]", got)
	}
}

// An empty array from the database is []any{}, not nil: the form tells
// "empty list" from "no value", and nil would render as a missing field.
func TestArrayDecodeEmptyIsEmptySlice(t *testing.T) {
	c := Strings(keywordsCol)
	got := c.decode([]any{})
	slice, ok := got.([]any)
	if !ok || slice == nil {
		t.Fatalf("decode([]any{}) = %#v, want empty []any", got)
	}
	if len(slice) != 0 {
		t.Errorf("decode = %v, want empty", slice)
	}
}

func TestArrayDecodeNilIsNil(t *testing.T) {
	c := Strings(keywordsCol)
	if got := c.decode(nil); got != nil {
		t.Errorf("decode(nil) = %#v, want nil", got)
	}
}

// TestArrayValueMatchesAssign pins that value and assign derive from one
// parse: the echo and the ok verdict must agree on every constructor (the
// same guarantee as the scalar cell in column.go).
func TestArrayValueMatchesAssign(t *testing.T) {
	cases := []struct {
		name string
		cell Column
		in   any
	}{
		{"strings", Strings(postgres.StringArrayColumn("tags")), []any{"a", "b"}},
		{"ints", Ints(postgres.IntegerArrayColumn("nums")), []any{1.0, 2.0}},
		{"floats", Floats(postgres.FloatArrayColumn("rates")), []any{1.5}},
		{"timestamps", Timestamps(postgres.TimestampzArrayColumn("stamps")), []any{"2026-08-06T10:00:00+05:00"}},
		{"strings null", Strings(postgres.StringArrayColumn("tags")), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expr, echoV, okV := ExportValue(c.cell, c.in)
			_, echoA, okA := ExportAssign(c.cell, c.in)
			if !okV || !okA {
				t.Fatalf("value ok=%v assign ok=%v, want both true", okV, okA)
			}
			if expr == nil {
				t.Fatal("value returned a nil expression")
			}
			if fmt.Sprint(echoV) != fmt.Sprint(echoA) {
				t.Errorf("echo diverged: value=%v assign=%v", echoV, echoA)
			}
		})
	}
}

// TestArrayValueRejectsUnparsedInput pins that a rejected input looks the
// same on both halves: ok=false, no panic.
func TestArrayValueRejectsUnparsedInput(t *testing.T) {
	cell := Strings(postgres.StringArrayColumn("tags"))
	if _, _, ok := ExportValue(cell, "not a list"); ok {
		t.Error("value accepted a non-list")
	}
	if _, _, ok := ExportValue(cell, []any{1.0}); ok {
		t.Error("value accepted a non-string element")
	}
}
