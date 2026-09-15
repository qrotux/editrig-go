// Package storetest is the conformance suite for storage implementations of
// the editor.
//
// The core asks a store for three functions over map[string]any
// (Entity.Load/Save/Delete) plus a write report in editrig.Change. Half of
// the requirements are not in the signatures but in behaviour: how "no row"
// is reported, whether an absent key differs from an explicit null, in what
// shape values arrive. A divergence there shows not as an error but as a
// silently wrong render or a false journal row on every save, so the
// contract is executable rather than described (compare fstest.TestFS).
//
// The suite is called from the implementation's own test in one line:
//
//	func TestConformance(t *testing.T) { storetest.Run(t, newSubject) }
//
// # Fixture
//
// The suite dictates the test entity so it can check behaviour per field. An
// implementation maps these six names onto its storage:
//
//	id       PK, string                    READ ONLY
//	title    string, required on create    WRITABLE
//	note     string, nullable              WRITABLE
//	counter  number, nullable              WRITABLE
//	touched  timestamp (RFC 3339), nullable WRITABLE
//	ro       string                        READ ONLY (owned by someone else)
//
// Plus four optional sub-suites with fields of their own: RunSatellite (a
// value outside the row), RunExtraTypes (integer, wall-clock, array columns),
// RunChildRows (a repeatable block as a separate table, one row per element)
// and RunKeyedChildRows (the same, partitioned by key).
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/qrotux/editrig-go"
)

// Fixture field names. Constants so a typo in an implementation's mapping is
// a compile error.
const (
	FieldID      = "id"
	FieldTitle   = "title"
	FieldNote    = "note"
	FieldCounter = "counter"
	FieldTouched = "touched"
	FieldRO      = "ro"
)

// Subject is the store under test: the three hooks of the core protocol plus
// two test-only instruments.
type Subject struct {
	Load   func(ctx context.Context, id string) (map[string]any, error)
	Save   func(ctx context.Context, id *string, in map[string]any) (outID string, ferr []editrig.FieldError, err error)
	Delete func(ctx context.Context, id string) error

	// Written returns the diff the store reported for the last write (what
	// went to OnWrite). The suite calls it right after Save.
	Written func() []editrig.Change

	// SetRaw puts a value in place bypassing Save. Needed for one check only:
	// a read-only field cannot be set through Save, yet the suite must prove
	// Save does not overwrite it, otherwise "read-only" is indistinguishable
	// from "wrote nil".
	SetRaw func(ctx context.Context, id, field string, value any) error
}

// New builds a clean store; it is called for every check, and the suite
// expects no state to leak between them.
type New func(t *testing.T) Subject

// Run executes the whole mandatory suite.
func Run(t *testing.T, newSubject New) {
	t.Helper()
	checks := []struct {
		name string
		fn   func(*testing.T, Subject)
	}{
		{"LoadOfUnknownIDIsNotAnError", loadUnknown},
		{"SaveCreatesAndReturnsID", createRoundTrip},
		{"UpdateIsPartial", partialUpdate},
		{"ExplicitNullClearsNullable", nullClears},
		{"ReadonlyFieldIsUnreachable", readonlyUnreachable},
		{"SaveOfUnknownIDIsErrNotFound", saveUnknown},
		{"ReadonlyOnlyPayloadIsNotAWrite", readonlyOnlyPayload},
		{"LoadedValuesSurviveJSONRoundTrip", wireForm},
		{"TimestampRoundTripsAsRFC3339", timestampWire},
		{"DiffReportsOnlyRealChanges", diffReal},
		{"DeleteRemovesAndIsIdempotent", deleteRow},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) { c.fn(t, newSubject(t)) })
	}
}

// --- checks -------------------------------------------------------------------

// loadUnknown: "no row" from Load is (nil, nil), not an error. The engine turns
// it into a 404; an error would turn the 404 into a 500.
func loadUnknown(t *testing.T, s Subject) {
	got, err := s.Load(context.Background(), "no-such-id")
	if err != nil {
		t.Fatalf("Load of an unknown id returned an error: %v — want (nil, nil)", err)
	}
	if got != nil {
		t.Errorf("Load of an unknown id returned %v — want nil", got)
	}
}

func createRoundTrip(t *testing.T, s Subject) {
	ctx := context.Background()
	id, ferr, err := s.Save(ctx, nil, map[string]any{FieldTitle: "first"})
	if err != nil || len(ferr) != 0 {
		t.Fatalf("create: err=%v ferr=%+v", err, ferr)
	}
	if id == "" {
		t.Fatal("create returned an empty id — the engine reloads the row by it")
	}
	row, err := s.Load(ctx, id)
	if err != nil || row == nil {
		t.Fatalf("load after create: err=%v row=%v", err, row)
	}
	if got := row[FieldTitle]; got != "first" {
		t.Errorf("%s = %v, want first", FieldTitle, got)
	}
	// The insert diff must carry what was created: the journal takes the
	// content of the entry from it.
	if changes := s.Written(); len(changes) == 0 {
		t.Error("create reported no changes — the audit entry would have no content")
	}
}

// partialUpdate: the essence of the write contract. A key absent from the
// payload is not touched at all, not rewritten "with the same value".
func partialUpdate(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "orig", FieldNote: "keep", FieldCounter: float64(7)})

	if _, _, err := s.Save(ctx, &id, map[string]any{FieldTitle: "changed"}); err != nil {
		t.Fatalf("partial save: %v", err)
	}
	row := load(t, s, id)
	if row[FieldTitle] != "changed" {
		t.Errorf("%s = %v, want changed", FieldTitle, row[FieldTitle])
	}
	if row[FieldNote] != "keep" {
		t.Errorf("%s = %v, want keep — an absent key must not be written", FieldNote, row[FieldNote])
	}
	if fmt.Sprint(row[FieldCounter]) != "7" {
		t.Errorf("%s = %v, want 7 — an absent key must not be written", FieldCounter, row[FieldCounter])
	}
}

// nullClears: an explicit JSON null means "clear" and must differ from an
// absent key; without the distinction clearing a field in the form would
// silently roll back.
func nullClears(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldNote: "filled", FieldCounter: float64(3)})

	if _, _, err := s.Save(ctx, &id, map[string]any{FieldNote: nil}); err != nil {
		t.Fatalf("null save: %v", err)
	}
	row := load(t, s, id)
	if row[FieldNote] != nil {
		t.Errorf("%s = %v, want nil — an explicit null must clear the field", FieldNote, row[FieldNote])
	}
	if fmt.Sprint(row[FieldCounter]) != "3" {
		t.Errorf("%s = %v, want 3 — clearing one field must not touch another", FieldCounter, row[FieldCounter])
	}
}

// readonlyUnreachable: a read-only field is physically unwritable. The engine
// strips such keys before Save (validate.StripReadonly), but that is the second line of
// defence; the first is here and must hold against a forged or stale payload.
func readonlyUnreachable(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t"})
	if err := s.SetRaw(ctx, id, FieldRO, "owned-by-worker"); err != nil {
		t.Fatalf("SetRaw: %v", err)
	}

	if _, _, err := s.Save(ctx, &id, map[string]any{FieldTitle: "t2", FieldRO: "stolen"}); err != nil {
		t.Fatalf("save with a read-only key: %v", err)
	}
	row := load(t, s, id)
	if row[FieldRO] != "owned-by-worker" {
		t.Errorf("%s = %v, want owned-by-worker — a read-only field must be unwritable", FieldRO, row[FieldRO])
	}
	// Nor may it appear in the diff: the journal must not report a write that
	// did not happen.
	for _, c := range s.Written() {
		if c.Field == FieldRO {
			t.Errorf("diff reports a read-only field: %+v", c)
		}
	}
}

// saveUnknown: "no row" from Save is editrig.ErrNotFound, which the core turns
// into a 404. Not a field error: an external implementation could not guess
// such a convention.
func saveUnknown(t *testing.T, s Subject) {
	_, ferr, err := s.Save(context.Background(), ptr("no-such-id"), map[string]any{FieldTitle: "x"})
	if !errors.Is(err, editrig.ErrNotFound) {
		t.Errorf("save of an unknown id: err = %v, want editrig.ErrNotFound", err)
	}
	if len(ferr) != 0 {
		t.Errorf("save of an unknown id reported a validation error: %+v — \"no row\" is not a 422", ferr)
	}
}

// readonlyOnlyPayload: a payload of read-only keys only has nothing to write.
// The branch is treacherous from both sides: on an existing row it must not
// fail (an empty "UPDATE SET" is invalid SQL), on a missing one it must return
// the same sentinel rather than a silent success with a foreign id.
func readonlyOnlyPayload(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t"})

	outID, ferr, err := s.Save(ctx, &id, map[string]any{FieldRO: "nope"})
	if err != nil || len(ferr) != 0 {
		t.Fatalf("read-only-only payload on an existing row: err=%v ferr=%+v", err, ferr)
	}
	if outID != id {
		t.Errorf("outID = %q, want %q", outID, id)
	}
	if _, _, err := s.Save(ctx, ptr("no-such-id"), map[string]any{FieldRO: "nope"}); !errors.Is(err, editrig.ErrNotFound) {
		t.Errorf("read-only-only payload on an unknown id: err = %v, want editrig.ErrNotFound", err)
	}
}

// wireForm is the least obvious part of the contract: values from Load go to
// JSON as they are, so each must be a JSON scalar, not a struct that becomes
// something else in JSON.
//
// Phrased as a round trip rather than a type whitelist so it catches the whole
// class: pgtype.Numeric would travel as an object, a [16]byte uuid as an array
// of numbers, []byte as a base64 string. Each of these "works" until the first
// render and the first diff.
func wireForm(t *testing.T, s Subject) {
	id := seed(t, s, map[string]any{
		FieldTitle:   "t",
		FieldNote:    "n",
		FieldCounter: float64(42),
		FieldTouched: "2026-01-02T03:04:05Z",
	})
	row := load(t, s, id)

	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("loaded row does not marshal to JSON: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("re-reading the marshalled row: %v", err)
	}
	for field, want := range row {
		got := back[field]
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s does not survive a JSON round-trip: %#v → %#v — "+
				"the value must be a JSON scalar, not a driver struct", field, want, got)
		}
	}
}

// timestampWire: a timestamp on the wire is an RFC 3339 string (ui.Timestamp
// declares it so and the write path parses it so). Returning anything else
// breaks both render and write silently, because the schema's format is not
// asserted by the server.
func timestampWire(t *testing.T, s Subject) {
	const stamp = "2026-01-02T03:04:05Z"
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldTouched: stamp})

	row := load(t, s, id)
	got, isString := row[FieldTouched].(string)
	if !isString {
		t.Fatalf("%s = %#v, want an RFC3339 string", FieldTouched, row[FieldTouched])
	}
	if got != stamp {
		t.Errorf("%s = %q, want %q — a timestamp must come back as it was sent", FieldTouched, got, stamp)
	}
}

// diffReal: the diff carries only the fields that actually changed. Saving the
// same value is not an event, or the journal would fill with noise on every
// submit of an unchanged form.
func diffReal(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "same", FieldNote: "before"})

	if _, _, err := s.Save(ctx, &id, map[string]any{FieldTitle: "same", FieldNote: "after"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	changes := s.Written()
	byField := map[string]editrig.Change{}
	for _, c := range changes {
		byField[c.Field] = c
	}
	if _, reported := byField[FieldTitle]; reported {
		t.Errorf("diff reports %s, whose value did not change: %+v", FieldTitle, changes)
	}
	c, reported := byField[FieldNote]
	if !reported {
		t.Fatalf("diff does not report %s, which changed: %+v", FieldNote, changes)
	}
	if fmt.Sprint(c.Old) != "before" || fmt.Sprint(c.New) != "after" {
		t.Errorf("%s change = %+v, want before→after", FieldNote, c)
	}
}

// deleteRow: delete removes the row, and deleting again is not an error; the
// engine checks existence through Load before calling.
func deleteRow(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t"})

	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	row, err := s.Load(ctx, id)
	if err != nil {
		t.Fatalf("load after delete: %v", err)
	}
	if row != nil {
		t.Errorf("row survived delete: %v", row)
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Errorf("second delete returned %v — deleting a missing row is not an error", err)
	}
}

// --- satellites (optional sub-suite) ------------------------------------------

// FieldLocalized is the satellite field of the fixture: its value lives
// outside the main row (in Postgres, a side table keyed by the parent id) and
// travels as a "locale -> string" map.
//
// Outside the six mandatory fields because not every store has satellites and
// Run is the contract for any store; extending the mandatory fixture would
// oblige every implementation to set up a second table before passing the
// base suite.
const FieldLocalized = "localized"

// RunSatellite is the suite for stores that support fields outside the main
// table. Called in addition to Run with the same factory:
//
//	func TestConformance(t *testing.T) {
//		storetest.Run(t, newSubject)
//		storetest.RunSatellite(t, newSubject)
//	}
//
// It checks exactly what is specific to a satellite: the "absent key means
// untouched" rule goes one level deeper (inside the map), a satellite-only
// payload is a real write rather than an empty Save, and all of it is still
// one transaction.
func RunSatellite(t *testing.T, newSubject New) {
	t.Helper()
	checks := []struct {
		name string
		fn   func(*testing.T, Subject)
	}{
		{"SatelliteRoundTrips", satelliteRoundTrip},
		{"AbsentKeyLeavesSatelliteAlone", satelliteAbsentKey},
		{"PartialMapMergesPerKey", satellitePartialMap},
		{"EmptyValueClearsOnlyItsKey", satelliteClears},
		{"SatelliteOnlyPayloadIsAWrite", satelliteOnlyPayload},
		{"SatelliteOnlyPayloadOnUnknownIDIsErrNotFound", satelliteUnknownID},
		{"SatelliteSentAtCreateSurvivesCommit", satelliteOnCreate},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) { c.fn(t, newSubject(t)) })
	}
}

// satelliteRoundTrip: what was written reads back in the same shape, a map of
// strings that survives a JSON round trip. The engine puts the value straight
// into the form document, and a driver type would break the render.
func satelliteRoundTrip(t *testing.T, s Subject) {
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldLocalized: map[string]any{"en": "hello", "ru": "привет"}})

	got := localized(t, load(t, s, id))
	if got["en"] != "hello" || got["ru"] != "привет" {
		t.Errorf("%s = %v, want {en:hello ru:привет}", FieldLocalized, got)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("%s does not survive JSON: %v", FieldLocalized, err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil || back["en"] != "hello" {
		t.Errorf("%s after JSON round trip = %v (err %v)", FieldLocalized, back, err)
	}
}

// satelliteAbsentKey: a satellite follows the same rule as columns; an absent
// key means "leave alone", not "erase".
func satelliteAbsentKey(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldLocalized: map[string]any{"en": "hello"}})

	if _, _, err := s.Save(ctx, &id, map[string]any{FieldTitle: "changed"}); err != nil {
		t.Fatalf("save without the satellite key: %v", err)
	}
	if got := localized(t, load(t, s, id)); got["en"] != "hello" {
		t.Errorf("%s = %v — an absent key must not clear the satellite", FieldLocalized, got)
	}
}

// satellitePartialMap: the "absent key means untouched" rule also applies
// inside the map; otherwise a form saved with one active locale would silently
// erase all the others on the first Save.
func satellitePartialMap(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldLocalized: map[string]any{"en": "hello", "ru": "привет"}})

	if _, _, err := s.Save(ctx, &id, map[string]any{FieldLocalized: map[string]any{"ru": "здравствуй"}}); err != nil {
		t.Fatalf("partial satellite save: %v", err)
	}
	got := localized(t, load(t, s, id))
	if got["ru"] != "здравствуй" {
		t.Errorf("%s[ru] = %v, want здравствуй", FieldLocalized, got["ru"])
	}
	if got["en"] != "hello" {
		t.Errorf("%s[en] = %v, want hello — a key absent from the map must survive", FieldLocalized, got["en"])
	}
}

// satelliteClears: an empty string clears exactly that key. The value
// disappears entirely (in Postgres, together with its side-table row): "empty"
// and "no entry" are indistinguishable on read, so there is no point keeping a
// placeholder.
func satelliteClears(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldLocalized: map[string]any{"en": "hello", "ru": "привет"}})

	if _, _, err := s.Save(ctx, &id, map[string]any{FieldLocalized: map[string]any{"ru": ""}}); err != nil {
		t.Fatalf("clearing save: %v", err)
	}
	got := localized(t, load(t, s, id))
	if v, present := got["ru"]; present && v != "" {
		t.Errorf("%s[ru] = %v, want cleared", FieldLocalized, v)
	}
	if got["en"] != "hello" {
		t.Errorf("%s[en] = %v — clearing one key must not touch another", FieldLocalized, got["en"])
	}
}

// satelliteOnlyPayload: a payload without a single column key is a write, not
// an empty Save. A partial-UPDATE implementation has no assignment here, and a
// naive query build yields `UPDATE ... SET WHERE`, a 500 on every save of
// such a field.
func satelliteOnlyPayload(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldLocalized: map[string]any{"en": "hello"}})

	if _, ferr, err := s.Save(ctx, &id, map[string]any{FieldLocalized: map[string]any{"en": "bye"}}); err != nil || len(ferr) != 0 {
		t.Fatalf("satellite-only save: err=%v ferr=%+v", err, ferr)
	}
	if got := localized(t, load(t, s, id)); got["en"] != "bye" {
		t.Errorf("%s = %v, want {en:bye}", FieldLocalized, got)
	}
	// The journal must learn about this write, or a satellite edit leaves no
	// audit trace.
	var seen bool
	for _, c := range s.Written() {
		seen = seen || c.Field == FieldLocalized
	}
	if !seen {
		t.Errorf("changes = %+v, want one for %s", s.Written(), FieldLocalized)
	}
}

// satelliteOnCreate: a satellite sent directly in the create payload (not via
// seed's separate create+update) must survive the commit. A store that commits
// right after the insert loses it silently: no error, no journal row, just
// NULL on the next Load.
func satelliteOnCreate(t *testing.T, s Subject) {
	ctx := context.Background()
	id, ferr, err := s.Save(ctx, nil, map[string]any{
		FieldTitle:     "t",
		FieldLocalized: map[string]any{"en": "hello"},
	})
	if err != nil || len(ferr) != 0 {
		t.Fatalf("create with a satellite field in the payload: err=%v ferr=%+v", err, ferr)
	}
	if got := localized(t, load(t, s, id)); got["en"] != "hello" {
		t.Errorf("%s = %v, want {en:hello} — a satellite sent at create must survive the commit", FieldLocalized, got)
	}
}

// satelliteUnknownID: "nothing to write to the columns" must not turn into "no
// row" or vice versa. A satellite is not written for a missing parent; the
// answer is ErrNotFound.
func satelliteUnknownID(t *testing.T, s Subject) {
	_, _, err := s.Save(context.Background(), ptr("no-such-id"), map[string]any{FieldLocalized: map[string]any{"en": "x"}})
	if !errors.Is(err, editrig.ErrNotFound) {
		t.Errorf("Save of a satellite for an unknown id = %v, want ErrNotFound", err)
	}
}

// localized returns the row's satellite value as a map of strings.
func localized(t *testing.T, row map[string]any) map[string]string {
	t.Helper()
	raw, ok := row[FieldLocalized]
	if !ok {
		t.Fatalf("row has no %s: %v", FieldLocalized, row)
	}
	return stringMap(t, FieldLocalized, raw)
}

// stringMap converts a map field to strings. Shared by the row satellite and
// the nested element satellite (itemText): their wire shape is the same, and
// two conversions would disagree on what counts as a valid map.
func stringMap(t *testing.T, field string, raw any) map[string]string {
	t.Helper()
	m, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("%s = %T, want map[string]any (the form document carries it as JSON)", field, raw)
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("%s[%s] = %T, want string", field, k, v)
		}
		out[k] = s
	}
	return out
}

// --- types beyond the mandatory ones (optional sub-suite) ---------------------

// Fields beyond the mandatory six. Separate for the same reason as the
// satellite: an integer column, a zoneless column and an array column are not
// something every store has, and Run is the contract for any store.
const (
	FieldQty      = "qty"      // integer
	FieldLocalAt  = "local_at" // wall-clock: time without a zone
	FieldKeywords = "keywords" // list of strings
	FieldNums     = "nums"     // list of integers
	FieldRates    = "rates"    // list of numbers
	FieldStamps   = "stamps"   // list of timestamps (RFC 3339)
)

// RunExtraTypes is the suite for stores that support types beyond the
// mandatory six. Called in addition to Run with the same factory:
//
//	func TestConformance(t *testing.T) {
//		storetest.Run(t, newSubject)
//		storetest.RunExtraTypes(t, newSubject)
//	}
//
// It checks what the mandatory fields cannot: an integer does not come back as
// another type; a wall-clock value gains no zone on the way; for an array an
// empty list and NULL are different states, order matters, duplicates survive,
// and the wire shape is a JSON array of scalars. One suite rather than three
// because an implementation either supports these types together with this
// fixture or does not call the suite at all.
func RunExtraTypes(t *testing.T, newSubject New) {
	t.Helper()
	checks := []struct {
		name string
		fn   func(*testing.T, Subject)
	}{
		{"IntegerRoundTripsAsNumber", integerRoundTrip},
		{"WallClockKeepsNoZone", wallClockRoundTrip},
		{"ArrayRoundTripsPreservingOrder", arrayRoundTrip},
		{"EmptyListIsNotNull", arrayEmptyVsNull},
		{"AbsentKeyLeavesArrayAlone", arrayAbsentKey},
		{"ArrayValuesSurviveJSONRoundTrip", arrayWireForm},
		{"EveryElementTypeRoundTrips", arrayElementTypes},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) { c.fn(t, newSubject(t)) })
	}
}

// integerRoundTrip: an integer must come back as a number that survives JSON.
// The driver returns int32/int64, and without normalization the two sides of
// the audit diff diverge, which only a live column reveals.
func integerRoundTrip(t *testing.T, s Subject) {
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldQty: float64(42)})
	row := load(t, s, id)

	if fmt.Sprint(row[FieldQty]) != "42" {
		t.Errorf("%s = %#v, want 42", FieldQty, row[FieldQty])
	}
	raw, err := json.Marshal(row[FieldQty])
	if err != nil || string(raw) != "42" {
		t.Errorf("%s marshals to %s (err %v), want 42 — must be a JSON number", FieldQty, raw, err)
	}
}

// wallClockRoundTrip: a zoneless column must return exactly the digits that
// were sent, no "Z" and no shift. This is the one check a unit test cannot
// fake: the zone appears on the way through the driver and the database.
func wallClockRoundTrip(t *testing.T, s Subject) {
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldLocalAt: "2026-08-05T10:00"})

	row := load(t, s, id)
	got, isString := row[FieldLocalAt].(string)
	if !isString {
		t.Fatalf("%s = %#v, want a string", FieldLocalAt, row[FieldLocalAt])
	}
	// Fatalf, not Errorf: got[10:] below panics on a short string, which is
	// exactly what a broken backend returns, and the test must report the
	// contract violation instead of panicking before the report.
	if got != "2026-08-05T10:00:00" {
		t.Fatalf("%s = %q, want 2026-08-05T10:00:00 — wall-clock must not gain a zone or drift",
			FieldLocalAt, got)
	}
	if strings.HasSuffix(got, "Z") || strings.Contains(got[10:], "+") {
		t.Errorf("%s = %q carries a zone — the column has none", FieldLocalAt, got)
	}
}

// arrayRoundTrip: order is part of the value and duplicates are not an error.
// Array elements are values, not relation ids: deduplication would lose data.
func arrayRoundTrip(t *testing.T, s Subject) {
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldKeywords: []any{"b", "a", "b"}})
	got := stringList(t, load(t, s, id), FieldKeywords)
	if len(got) != 3 || got[0] != "b" || got[1] != "a" || got[2] != "b" {
		t.Errorf("%s = %v, want [b a b] — order and duplicates are part of the value", FieldKeywords, got)
	}
}

// arrayEmptyVsNull: two different kinds of "empty". Collapsing them would take
// away the editor's ability to set a nullable column back to NULL.
func arrayEmptyVsNull(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldKeywords: []any{"a"}})

	if _, _, err := s.Save(ctx, &id, map[string]any{FieldKeywords: []any{}}); err != nil {
		t.Fatalf("save of an empty list: %v", err)
	}
	got := load(t, s, id)[FieldKeywords]
	list, ok := got.([]any)
	if !ok || list == nil {
		t.Fatalf("%s = %#v, want an empty list, not nil", FieldKeywords, got)
	}
	if len(list) != 0 {
		t.Errorf("%s = %v, want empty", FieldKeywords, list)
	}

	if _, _, err := s.Save(ctx, &id, map[string]any{FieldKeywords: nil}); err != nil {
		t.Fatalf("save of an explicit null: %v", err)
	}
	if got := load(t, s, id)[FieldKeywords]; got != nil {
		t.Errorf("%s = %#v, want nil — an explicit null must clear the column", FieldKeywords, got)
	}
}

// arrayAbsentKey: an array follows the same "absent key means untouched" rule
// as scalar columns; saving another field must not silently erase a list that
// was not in the payload.
func arrayAbsentKey(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldKeywords: []any{"keep"}})

	if _, _, err := s.Save(ctx, &id, map[string]any{FieldTitle: "changed"}); err != nil {
		t.Fatalf("save without the array key: %v", err)
	}
	if got := stringList(t, load(t, s, id), FieldKeywords); len(got) != 1 || got[0] != "keep" {
		t.Errorf("%s = %v — an absent key must not clear the array", FieldKeywords, got)
	}
}

// arrayWireForm: the same class as wireForm for scalars. A driver array type
// (pq.StringArray, pgtype.FlatArray) would not travel to JSON as an array.
func arrayWireForm(t *testing.T, s Subject) {
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldKeywords: []any{"a", "b"}})
	row := load(t, s, id)

	raw, err := json.Marshal(row[FieldKeywords])
	if err != nil {
		t.Fatalf("%s does not marshal to JSON: %v", FieldKeywords, err)
	}
	if string(raw) != `["a","b"]` {
		t.Errorf("%s marshals to %s, want [\"a\",\"b\"] — the form carries it as a JSON array", FieldKeywords, raw)
	}
}

// arrayElementTypes: every element type must survive the round trip in its
// wire shape: numbers as numbers, timestamps as RFC 3339 strings.
func arrayElementTypes(t *testing.T, s Subject) {
	id := seed(t, s, map[string]any{
		FieldTitle:  "t",
		FieldNums:   []any{float64(1), float64(2)},
		FieldRates:  []any{1.5, 2.25},
		FieldStamps: []any{"2026-01-02T03:04:05Z"},
	})
	row := load(t, s, id)

	if got := fmt.Sprint(row[FieldNums]); got != "[1 2]" {
		t.Errorf("%s = %v, want [1 2]", FieldNums, row[FieldNums])
	}
	if got := fmt.Sprint(row[FieldRates]); got != "[1.5 2.25]" {
		t.Errorf("%s = %v, want [1.5 2.25]", FieldRates, row[FieldRates])
	}
	stamps := stringList(t, row, FieldStamps)
	if len(stamps) != 1 || stamps[0] != "2026-01-02T03:04:05Z" {
		t.Errorf("%s = %v, want [2026-01-02T03:04:05Z] — a timestamp must come back as an RFC 3339 string",
			FieldStamps, stamps)
	}
}

// stringList converts an array field to strings.
func stringList(t *testing.T, row map[string]any, field string) []string {
	t.Helper()
	raw, ok := row[field]
	if !ok {
		t.Fatalf("row has no %s: %v", field, row)
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s = %T, want []any (the form document carries it as a JSON array)", field, raw)
	}
	out := make([]string, len(list))
	for i, v := range list {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("%s[%d] = %T, want string", field, i, v)
		}
		out[i] = s
	}
	return out
}

// --- repeatable blocks (optional sub-suite) -----------------------------------

// Repeatable block fields. Separate from the mandatory six for the same reason
// as the satellite: not every store has child tables (a document database
// nests the block as an array), and Run is the contract for any store.
const (
	FieldValues = "values" // list of scalars in a child table, one list per key
	FieldItems  = "items"  // list of objects; an element carries an id, a number, a translation, an array and a jsonb document
)

// Subfields of a FieldItems element. ItemFieldID has the same value as FieldID
// but is a different name: the element id lives in the object's namespace, not
// the row's, and one constant for both would claim that renaming one renames
// the other.
const (
	ItemFieldID   = "id"   // READ ONLY: identity of the element row
	ItemFieldQty  = "qty"  // number
	ItemFieldText = "text" // nested satellite of the element: "locale -> string" map
	ItemFieldTags = "tags" // array column inside the element (text[])
	ItemFieldMeta = "meta" // jsonb inside the element, edited as a whole
)

// Field and subfields of the keyed object block fixture (RunKeyedChildRows):
// an object element plus a partition key in the row itself.
const (
	FieldBlocks     = "blocks"
	BlockFieldID    = "id"    // READ ONLY: identity of the element row
	BlockFieldTitle = "title" // string, required
	BlockFieldTags  = "tags"  // array column inside the element (text[])
)

// RunChildRows is the suite for stores where a repeatable block lives in a
// separate table, one row per element. Called in addition to Run with the
// same factory:
//
//	func TestConformance(t *testing.T) {
//		storetest.Run(t, newSubject)
//		storetest.RunChildRows(t, newSubject)
//	}
//
// It checks what neither an array column nor a satellite map has: an element
// has an identity of its own that must survive a save (its own nested rows may
// reference it) while the list is rebuilt as a whole.
//
// The fixture has two fields because a repeatable block has two independent
// axes: the element is a scalar (FieldValues) or an object (FieldItems), and
// the table is partitioned (one list per key) or not. An implementation that
// supports only one axis does not call this suite.
func RunChildRows(t *testing.T, newSubject New) {
	t.Helper()
	checks := []struct {
		name string
		fn   func(*testing.T, Subject)
	}{
		{"ValuesRoundTripInOrder", childValuesRoundTrip},
		{"ValuesPartitionIsIndependent", childValuesPartition},
		{"ItemsRoundTrip", childItemsRoundTrip},
		{"ItemIDsSurviveReorder", childItemIDsSurvive},
		{"AppendedElementDoesNotStealASibling", childItemAppended},
		{"RemovedItemDisappears", childItemRemoved},
		{"NestedSatelliteSurvivesReorder", childNestedSatellite},
		{"ForeignItemIDBecomesANewElement", childForeignID},
		{"EmptyListClearsTheBlock", childEmptyList},
		{"ArrayAndJSONInsideElement", childItemArrayJSON},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) { c.fn(t, newSubject(t)) })
	}
}

// childValuesRoundTrip: order is part of the value and duplicates are not an
// error, as in arrayRoundTrip. A separate check because here the order is not
// stored with the value: it lives in its own position column, which the
// implementation must both write and sort by on read.
func childValuesRoundTrip(t *testing.T, s Subject) {
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldValues: map[string]any{
		"en": []any{"b", "a", "b"},
	}})

	got := valueLists(t, load(t, s, id))["en"]
	if len(got) != 3 || got[0] != "b" || got[1] != "a" || got[2] != "b" {
		t.Errorf("%s[en] = %v, want [b a b] — position is stored in its own column and must be read back through it",
			FieldValues, got)
	}
}

// childValuesPartition: in a partitioned table the lists of different keys are
// independent. The check catches a replacement done without a key filter: it
// would DELETE every key at once and reinsert only the one sent. No other
// check sees this; childValuesRoundTrip uses a single key, where an unfiltered
// DELETE is indistinguishable from the correct one.
func childValuesPartition(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldValues: map[string]any{
		"en": []any{"e1", "e2"},
		"ru": []any{"r1"},
	}})

	if _, _, err := s.Save(ctx, &id, map[string]any{FieldValues: map[string]any{
		"en": []any{"e3"},
	}}); err != nil {
		t.Fatalf("save of one partition: %v", err)
	}

	got := valueLists(t, load(t, s, id))
	if len(got["en"]) != 1 || got["en"][0] != "e3" {
		t.Errorf("%s[en] = %v, want [e3]", FieldValues, got["en"])
	}
	if len(got["ru"]) != 1 || got["ru"][0] != "r1" {
		t.Errorf("%s[ru] = %v, want [r1] — a key absent from the payload must not be touched",
			FieldValues, got["ru"])
	}
}

// childItemsRoundTrip: an object element comes back as an object, in the
// declared order, each with its own non-empty id. Without ids (or with the
// same id on all) the next save cannot tell elements apart, and the list
// itself round-trips either way.
//
// The wire shape is checked here rather than in wireForm: that one walks the
// row's fields and compares each as a whole through fmt.Sprint, so it could
// not name a missing or substituted subfield of an element. Here the JSON
// round trip is checked per subfield.
func childItemsRoundTrip(t *testing.T, s Subject) {
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldItems: []any{
		map[string]any{ItemFieldQty: 1.0},
		map[string]any{ItemFieldQty: 2.0},
		map[string]any{ItemFieldQty: 3.0},
	}})
	list := itemList(t, load(t, s, id))

	if len(list) != 3 {
		t.Fatalf("%s has %d elements, want 3: %v", FieldItems, len(list), list)
	}
	for i, want := range []string{"1", "2", "3"} {
		if got := fmt.Sprint(list[i][ItemFieldQty]); got != want {
			t.Errorf("%s[%d].%s = %v, want %s — order is part of the value", FieldItems, i, ItemFieldQty, got, want)
		}
	}
	seen := map[string]bool{}
	for i, item := range list {
		id, _ := item[ItemFieldID].(string)
		if id == "" {
			t.Errorf("%s[%d] came back without %s — the next save cannot tell elements apart",
				FieldItems, i, ItemFieldID)
			continue
		}
		if seen[id] {
			t.Errorf("%s[%d].%s = %q is not unique — two elements share one identity", FieldItems, i, ItemFieldID, id)
		}
		seen[id] = true
	}

	// Per subfield: a "starts with [{" check would be a tautology after
	// itemList established the slice shape. The real question is whether every
	// declared subfield arrives with the same value: a driver type inside the
	// element (pgtype.Numeric for qty, [16]byte for id) survives Marshal but
	// arrives as an object or an array of numbers.
	raw, err := json.Marshal(load(t, s, id)[FieldItems])
	if err != nil {
		t.Fatalf("%s does not marshal to JSON: %v", FieldItems, err)
	}
	var back []map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("re-reading the marshalled %s: %v", FieldItems, err)
	}
	if len(back) != len(list) {
		t.Fatalf("%s has %d elements after a JSON round trip, want %d", FieldItems, len(back), len(list))
	}
	for i, item := range list {
		for _, sub := range []string{ItemFieldID, ItemFieldQty, ItemFieldText} {
			want, declared := item[sub]
			if !declared {
				t.Errorf("%s[%d] has no %s — every declared subfield must reach the form", FieldItems, i, sub)
				continue
			}
			if got := back[i][sub]; fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("%s[%d].%s does not survive a JSON round trip: %#v → %#v — "+
					"the value must be a JSON scalar, not a driver struct", FieldItems, i, sub, want, got)
			}
		}
	}
}

// childItemIDsSurvive is the central check of the suite. A replacement
// (DELETE+INSERT) keeps the list itself green: elements present, order right,
// values the same. Only the ids turn it red, which is why the check stands on
// them rather than on the list's content.
func childItemIDsSurvive(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldItems: []any{
		map[string]any{ItemFieldQty: 1.0},
		map[string]any{ItemFieldQty: 2.0},
	}})

	before := itemIDs(t, load(t, s, id))
	if len(before) != 2 {
		t.Fatalf("seed produced %d items, want 2", len(before))
	}

	// Swap the two, keeping their ids.
	if _, _, err := s.Save(ctx, &id, map[string]any{FieldItems: []any{
		map[string]any{ItemFieldID: before[1], ItemFieldQty: 2.0},
		map[string]any{ItemFieldID: before[0], ItemFieldQty: 1.0},
	}}); err != nil {
		t.Fatalf("reorder: %v", err)
	}

	after := itemIDs(t, load(t, s, id))
	if len(after) != 2 || after[0] != before[1] || after[1] != before[0] {
		t.Errorf("ids after reorder = %v, want %v reversed — rows were replaced, not updated", after, before)
	}
	// And the order must be the new one: an implementation that keeps identity
	// but does not rewrite position returns the same ids in the old order, so
	// dragging elements would never be saved.
	list := itemList(t, load(t, s, id))
	if len(list) == 2 && fmt.Sprint(list[0][ItemFieldQty]) != "2" {
		t.Errorf("%s after reorder = %v, want qty 2 first — position was not rewritten", FieldItems, list)
	}
}

// childItemAppended: a mixed payload, known ids and a new element in one list,
// the edit a repeatable block exists for. Every other check sends homogeneous
// lists: all elements without ids (seed) or all with ids (reorder, removal),
// and the one id-carrying single-element payload lands on an empty list
// (childForeignID).
//
// It catches an implementation that resolves identity positionally (or whose
// bookkeeping of taken ids is leaky), so the new element upserts onto an
// existing sibling's row: that row's nested rows, keyed by its id, silently
// become the new element's, and the displaced element loses its own. The new
// element sits between the known ones, not at the tail: a positional
// implementation would get a tail append right by accident.
func childItemAppended(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldItems: []any{
		map[string]any{ItemFieldQty: 1.0, ItemFieldText: map[string]any{"en": "one"}},
		map[string]any{ItemFieldQty: 2.0, ItemFieldText: map[string]any{"en": "two"}},
	}})
	before := itemIDs(t, load(t, s, id))
	if len(before) != 2 {
		t.Fatalf("seed produced %d items, want 2", len(before))
	}

	// The translation key is deliberately absent from the payload, as in
	// childNestedSatellite: sending it would rewrite the translations
	// regardless of which element they ended up with.
	if _, _, err := s.Save(ctx, &id, map[string]any{FieldItems: []any{
		map[string]any{ItemFieldID: before[0], ItemFieldQty: 1.0},
		map[string]any{ItemFieldQty: 3.0},
		map[string]any{ItemFieldID: before[1], ItemFieldQty: 2.0},
	}}); err != nil {
		t.Fatalf("append into a non-empty list: %v", err)
	}

	after := itemList(t, load(t, s, id))
	if len(after) != 3 {
		t.Fatalf("%s has %d elements after the append, want 3: %v", FieldItems, len(after), after)
	}
	ids := itemIDs(t, load(t, s, id))
	if ids[0] != before[0] || ids[2] != before[1] {
		t.Errorf("ids after the append = %v, want %v around a fresh one — appending must not renumber siblings",
			ids, before)
	}
	if ids[1] == "" {
		t.Errorf("the appended element came back without %s", ItemFieldID)
	}
	if ids[1] == before[0] || ids[1] == before[1] {
		t.Errorf("the appended element reused an existing identity (%q) — it upserted onto a sibling's row, "+
			"and that sibling's nested rows now belong to the new element", ids[1])
	}

	// The translations stayed with their own elements. This is where a swapped
	// identity becomes visible: the list above could match even with one.
	if got := itemText(t, after[0]); got["en"] != "one" {
		t.Errorf("%s[0].%s = %v, want {en:one} — the first element lost or swapped its nested rows",
			FieldItems, ItemFieldText, got)
	}
	if got := itemText(t, after[2]); got["en"] != "two" {
		t.Errorf("%s[2].%s = %v, want {en:two} — the displaced element lost its nested rows",
			FieldItems, ItemFieldText, got)
	}
	if got := itemText(t, after[1]); len(got) != 0 {
		t.Errorf("the appended element inherited nested rows it never had: %s = %v — "+
			"its id was taken from an existing sibling", ItemFieldText, got)
	}
	if qty := fmt.Sprint(after[1][ItemFieldQty]); qty != "3" {
		t.Errorf("%s[1].%s = %v, want 3", FieldItems, ItemFieldQty, qty)
	}
}

// childItemRemoved: an element absent from the payload must not remain in the
// store. The converse of childItemIDsSurvive: an implementation that only
// upserts (and therefore keeps ids perfectly) fails here, the removed element
// silently returns to the form on the next Load.
func childItemRemoved(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldItems: []any{
		map[string]any{ItemFieldQty: 1.0},
		map[string]any{ItemFieldQty: 2.0},
		map[string]any{ItemFieldQty: 3.0},
	}})
	before := itemIDs(t, load(t, s, id))
	if len(before) != 3 {
		t.Fatalf("seed produced %d items, want 3", len(before))
	}

	// The middle element is dropped, the outer two are sent with their ids.
	if _, _, err := s.Save(ctx, &id, map[string]any{FieldItems: []any{
		map[string]any{ItemFieldID: before[0], ItemFieldQty: 1.0},
		map[string]any{ItemFieldID: before[2], ItemFieldQty: 3.0},
	}}); err != nil {
		t.Fatalf("save without the middle element: %v", err)
	}

	after := itemIDs(t, load(t, s, id))
	if len(after) != 2 || after[0] != before[0] || after[1] != before[2] {
		t.Errorf("ids after removal = %v, want %v — the dropped element must not survive",
			after, []string{before[0], before[2]})
	}
}

// childNestedSatellite is the reason element identity must survive at all: an
// element row has nested rows of its own (a translation parented on its id
// with ON DELETE CASCADE), and a replacement cascades them away on every save.
//
// It differs from childItemIDsSurvive by the consequence it catches: an id
// could be "kept" by rewriting the row (DELETE+INSERT with the same id), which
// matches list and ids but loses the translations. So the translations
// themselves are re-read and recounted, and the reorder payload deliberately
// omits the translation key: sending it would rewrite them regardless of
// whether they survived.
func childNestedSatellite(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldItems: []any{
		map[string]any{ItemFieldQty: 1.0, ItemFieldText: map[string]any{"en": "one", "ru": "один"}},
		map[string]any{ItemFieldQty: 2.0, ItemFieldText: map[string]any{"en": "two", "ru": "два"}},
	}})

	before := itemList(t, load(t, s, id))
	if len(before) != 2 {
		t.Fatalf("seed produced %d items, want 2", len(before))
	}
	for i, item := range before {
		if got := itemText(t, item); len(got) != 2 {
			t.Fatalf("%s[%d].%s = %v, want two locales — seed did not write the nested rows",
				FieldItems, i, ItemFieldText, got)
		}
	}
	beforeIDs := itemIDs(t, load(t, s, id))

	// Reorder without the translation key: only preserved element identity can
	// keep the nested rows.
	if _, _, err := s.Save(ctx, &id, map[string]any{FieldItems: []any{
		map[string]any{ItemFieldID: beforeIDs[1], ItemFieldQty: 2.0},
		map[string]any{ItemFieldID: beforeIDs[0], ItemFieldQty: 1.0},
	}}); err != nil {
		t.Fatalf("reorder: %v", err)
	}

	after := itemList(t, load(t, s, id))
	if len(after) != 2 {
		t.Fatalf("%s has %d elements after reorder, want 2", FieldItems, len(after))
	}
	want := []map[string]string{
		{"en": "two", "ru": "два"},
		{"en": "one", "ru": "один"},
	}
	for i, item := range after {
		got := itemText(t, item)
		if len(got) != len(want[i]) {
			t.Errorf("%s[%d].%s has %d locales after the reorder, want %d — the nested rows were cascade-deleted",
				FieldItems, i, ItemFieldText, len(got), len(want[i]))
			continue
		}
		for k, v := range want[i] {
			if got[k] != v {
				t.Errorf("%s[%d].%s[%s] = %q, want %q — the nested rows followed the wrong element",
					FieldItems, i, ItemFieldText, k, got[k], v)
			}
		}
	}
}

// childForeignID: an element id comes from the form and is not taken on
// trust. The engine strips top-level read-only fields before Save, but there
// is no such cleanup inside an element, so a forged or stale id reaches the
// store as is. An upsert by global PK would silently steal another parent's
// row: the victim's element would vanish while the attacker's list looked
// perfectly normal.
func childForeignID(t *testing.T, s Subject) {
	victim := seed(t, s, map[string]any{FieldTitle: "victim", FieldItems: []any{
		map[string]any{ItemFieldQty: 1.0},
	}})
	victimIDs := itemIDs(t, load(t, s, victim))
	if len(victimIDs) != 1 {
		t.Fatalf("victim seed produced %d items, want 1", len(victimIDs))
	}

	attacker := seed(t, s, map[string]any{FieldTitle: "attacker", FieldItems: []any{
		map[string]any{ItemFieldID: victimIDs[0], ItemFieldQty: 99.0},
	}})

	got := itemList(t, load(t, s, attacker))
	if len(got) != 1 {
		t.Fatalf("attacker has %d items, want 1: %v", len(got), got)
	}
	if id, _ := got[0][ItemFieldID].(string); id == victimIDs[0] {
		t.Errorf("a foreign element id was accepted as this parent's own (%q) — "+
			"an unknown id must degrade into a new element", id)
	}

	survivor := itemList(t, load(t, s, victim))
	if len(survivor) != 1 {
		t.Fatalf("the other parent has %d items after the foreign save, want 1: %v", len(survivor), survivor)
	}
	if id, _ := survivor[0][ItemFieldID].(string); id != victimIDs[0] {
		t.Errorf("the other parent's element id = %q, want %q", id, victimIDs[0])
	}
	if qty := fmt.Sprint(survivor[0][ItemFieldQty]); qty != "1" {
		t.Errorf("the other parent's element was overwritten: %s = %v, want 1", ItemFieldQty, qty)
	}
}

// childEmptyList: an empty list is legal input (every element removed) and
// must clear the block rather than be ignored as "nothing to write". The
// second save in a row exercises the same branch with an empty keep set: in
// SQL, NOT IN () is a syntax error, a 500 on every such form that no check
// with a non-empty list sees.
//
// An empty list reads back as an empty array, not nil: the form field is a
// list, and null would reach the widget instead of one.
func childEmptyList(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldItems: []any{
		map[string]any{ItemFieldQty: 1.0},
		map[string]any{ItemFieldQty: 2.0},
	}})

	if _, _, err := s.Save(ctx, &id, map[string]any{FieldItems: []any{}}); err != nil {
		t.Fatalf("save of an empty list: %v", err)
	}
	raw := load(t, s, id)[FieldItems]
	list, ok := raw.([]any)
	if !ok || list == nil {
		t.Fatalf("%s = %#v, want an empty list, not nil", FieldItems, raw)
	}
	if len(list) != 0 {
		t.Errorf("%s = %v, want empty — an empty list must clear the block", FieldItems, list)
	}

	if _, _, err := s.Save(ctx, &id, map[string]any{FieldItems: []any{}}); err != nil {
		t.Errorf("second save of an empty list: %v — an empty keep-set is a legal input", err)
	}

	// The same clear on a partitioned block, the very input on which an
	// unfiltered DELETE loses data silently: the user clears one locale's
	// field and every locale goes. childValuesPartition cannot see it, there
	// the replacement is a non-empty list.
	if _, _, err := s.Save(ctx, &id, map[string]any{FieldValues: map[string]any{
		"en": []any{"e1"},
		"ru": []any{"r1"},
	}}); err != nil {
		t.Fatalf("fill both partitions: %v", err)
	}
	if _, _, err := s.Save(ctx, &id, map[string]any{FieldValues: map[string]any{
		"en": []any{},
	}}); err != nil {
		t.Fatalf("clearing one partition: %v", err)
	}
	got := valueLists(t, load(t, s, id))
	// A cleared partition either disappears from the map or arrives as an
	// empty list; both are legal, so key presence is checked explicitly. The
	// asymmetry with FieldItems above is deliberate: for a list field
	// emptiness must be an empty array, while for a map field "no rows" and
	// "empty value" are indistinguishable on read (see satelliteClears), and
	// which keys to show empty is the schema's decision, not the store's.
	if list, present := got["en"]; present && len(list) != 0 {
		t.Errorf("%s[en] = %v, want cleared — an empty list must clear the partition", FieldValues, list)
	}
	if len(got["ru"]) != 1 || got["ru"][0] != "r1" {
		t.Errorf("%s[ru] = %v, want [r1] — clearing one partition must not touch another",
			FieldValues, got["ru"])
	}
}

// childItemArrayJSON: an array and a jsonb inside an element go through both
// write paths. The INSERT of a new element and the DO UPDATE of an existing
// one must write both columns, and the element id must survive the update.
func childItemArrayJSON(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldItems: []any{
		map[string]any{ItemFieldQty: 1.0, ItemFieldTags: []any{"a", "b"}, ItemFieldMeta: map[string]any{"k": "v"}},
	}})

	list := itemList(t, load(t, s, id))
	if len(list) != 1 {
		t.Fatalf("%s has %d elements, want 1", FieldItems, len(list))
	}
	tags, _ := list[0][ItemFieldTags].([]any)
	if len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Errorf("%s = %v, want [a b] — the INSERT path must write the array column", ItemFieldTags, list[0][ItemFieldTags])
	}
	meta, _ := list[0][ItemFieldMeta].(map[string]any)
	if fmt.Sprint(meta["k"]) != "v" {
		t.Errorf("%s = %v, want map[k:v] — the INSERT path must write the jsonb column", ItemFieldMeta, list[0][ItemFieldMeta])
	}

	itemID, _ := list[0][ItemFieldID].(string)
	if _, _, err := s.Save(ctx, &id, map[string]any{FieldItems: []any{
		map[string]any{ItemFieldID: itemID, ItemFieldQty: 1.0,
			ItemFieldTags: []any{"x"}, ItemFieldMeta: map[string]any{"k": "v2"}},
	}}); err != nil {
		t.Fatalf("update of an existing element: %v", err)
	}
	after := itemList(t, load(t, s, id))
	if len(after) != 1 {
		t.Fatalf("%s after update = %v, want 1 element", FieldItems, after)
	}
	if gotID, _ := after[0][ItemFieldID].(string); gotID != itemID {
		t.Fatalf("id changed on update: %q → %q — the row was replaced", itemID, gotID)
	}
	if tags, _ := after[0][ItemFieldTags].([]any); len(tags) != 1 || tags[0] != "x" {
		t.Errorf("%s after update = %v, want [x] — the DO UPDATE path must write the array column", ItemFieldTags, after[0][ItemFieldTags])
	}
	meta, _ = after[0][ItemFieldMeta].(map[string]any)
	if fmt.Sprint(meta["k"]) != "v2" {
		t.Errorf("%s after update = %v — the DO UPDATE path must write the jsonb column", ItemFieldMeta, after[0][ItemFieldMeta])
	}
}

// itemList converts the object list field to a slice of maps.
func itemList(t *testing.T, row map[string]any) []map[string]any {
	t.Helper()
	raw, ok := row[FieldItems]
	if !ok {
		t.Fatalf("row has no %s: %v", FieldItems, row)
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s = %T, want []any (the form document carries it as a JSON array)", FieldItems, raw)
	}
	out := make([]map[string]any, len(list))
	for i, v := range list {
		item, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%s[%d] = %T, want map[string]any — an element of a repeatable block is an object", FieldItems, i, v)
		}
		out[i] = item
	}
	return out
}

// itemIDs returns the element identities in list order.
func itemIDs(t *testing.T, row map[string]any) []string {
	t.Helper()
	list := itemList(t, row)
	out := make([]string, len(list))
	for i, item := range list {
		id, ok := item[ItemFieldID].(string)
		if !ok {
			t.Fatalf("%s[%d].%s = %T, want string", FieldItems, i, ItemFieldID, item[ItemFieldID])
		}
		out[i] = id
	}
	return out
}

// itemText returns the element's nested satellite as a map of strings.
func itemText(t *testing.T, item map[string]any) map[string]string {
	t.Helper()
	raw, ok := item[ItemFieldText]
	if !ok {
		t.Fatalf("element has no %s: %v", ItemFieldText, item)
	}
	return stringMap(t, FieldItems+"."+ItemFieldText, raw)
}

// valueLists converts the map-of-lists field to strings.
func valueLists(t *testing.T, row map[string]any) map[string][]string {
	t.Helper()
	raw, ok := row[FieldValues]
	if !ok {
		t.Fatalf("row has no %s: %v", FieldValues, row)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("%s = %T, want map[string]any (the form document carries it as a JSON object)", FieldValues, raw)
	}
	out := make(map[string][]string, len(m))
	for key, v := range m {
		list, ok := v.([]any)
		if !ok {
			t.Fatalf("%s[%s] = %T, want []any", FieldValues, key, v)
		}
		vals := make([]string, len(list))
		for i, e := range list {
			s, ok := e.(string)
			if !ok {
				t.Fatalf("%s[%s][%d] = %T, want string", FieldValues, key, i, e)
			}
			vals[i] = s
		}
		out[key] = vals
	}
	return out
}

// RunKeyedChildRows is the suite for a keyed object block: one list of objects
// per partition key (erjet.KeyedChildRows). The first three checks repeat the
// RunChildRows invariants within one partition; the rest pin the "absent key
// means untouched" rule at the partition level, as for KeyedChildValues.
func RunKeyedChildRows(t *testing.T, newSubject New) {
	t.Helper()
	checks := []struct {
		name string
		fn   func(*testing.T, Subject)
	}{
		{"RoundTripPerKey", keyedBlocksRoundTrip},
		{"PartitionIsIndependent", keyedBlocksPartition},
		{"IDsSurviveReorderWithinPartition", keyedBlocksIDsSurvive},
		{"EmptyKeyIsIgnored", keyedBlocksEmptyKey},
		{"GarbagePartitionValueIsIgnored", keyedBlocksGarbage},
		{"EmptyListClearsOnlyItsPartition", keyedBlocksEmptyList},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) { c.fn(t, newSubject(t)) })
	}
}

// blockLists converts FieldBlocks to a "key -> list of objects" map.
func blockLists(t *testing.T, row map[string]any) map[string][]map[string]any {
	t.Helper()
	raw, ok := row[FieldBlocks].(map[string]any)
	if !ok {
		t.Fatalf("%s = %T, want map", FieldBlocks, row[FieldBlocks])
	}
	out := make(map[string][]map[string]any, len(raw))
	for key, v := range raw {
		list, ok := v.([]any)
		if !ok {
			t.Fatalf("%s[%s] = %T, want list", FieldBlocks, key, v)
		}
		items := make([]map[string]any, 0, len(list))
		for i, el := range list {
			item, ok := el.(map[string]any)
			if !ok {
				t.Fatalf("%s[%s][%d] = %T, want object", FieldBlocks, key, i, el)
			}
			items = append(items, item)
		}
		out[key] = items
	}
	return out
}

// keyedBlocksRoundTrip: order and values per key, ids non-empty and globally
// unique (one PK per table), the array written by the INSERT path.
func keyedBlocksRoundTrip(t *testing.T, s Subject) {
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldBlocks: map[string]any{
		"en": []any{
			map[string]any{BlockFieldTitle: "one", BlockFieldTags: []any{"a", "b"}},
			map[string]any{BlockFieldTitle: "two"},
		},
		"ru": []any{map[string]any{BlockFieldTitle: "раз"}},
	}})

	got := blockLists(t, load(t, s, id))
	if len(got["en"]) != 2 || len(got["ru"]) != 1 {
		t.Fatalf("%s = %v, want 2 en + 1 ru", FieldBlocks, got)
	}
	if got["en"][0][BlockFieldTitle] != "one" || got["en"][1][BlockFieldTitle] != "two" {
		t.Errorf("en = %v — order is part of the value", got["en"])
	}
	tags, _ := got["en"][0][BlockFieldTags].([]any)
	if len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Errorf("en[0].%s = %v, want [a b] — the array column must survive the INSERT path",
			BlockFieldTags, got["en"][0][BlockFieldTags])
	}
	seen := map[string]bool{}
	for key, list := range got {
		for i, item := range list {
			bid, _ := item[BlockFieldID].(string)
			if bid == "" {
				t.Errorf("%s[%s][%d] came back without %s", FieldBlocks, key, i, BlockFieldID)
				continue
			}
			if seen[bid] {
				t.Errorf("%s[%s][%d].%s = %q is not unique", FieldBlocks, key, i, BlockFieldID, bid)
			}
			seen[bid] = true
		}
	}
}

// keyedBlocksPartition: saving one partition leaves another untouched, and an
// existing id goes through DO UPDATE, array column included.
func keyedBlocksPartition(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldBlocks: map[string]any{
		"en": []any{map[string]any{BlockFieldTitle: "one", BlockFieldTags: []any{"a"}}},
		"ru": []any{map[string]any{BlockFieldTitle: "раз"}},
	}})
	before := blockLists(t, load(t, s, id))
	if len(before["en"]) != 1 || len(before["ru"]) != 1 {
		t.Fatalf("seed = %v, want 1 en + 1 ru", before)
	}
	enID, _ := before["en"][0][BlockFieldID].(string)
	if enID == "" {
		t.Fatal("seed produced an en element without id")
	}

	if _, _, err := s.Save(ctx, &id, map[string]any{FieldBlocks: map[string]any{
		"en": []any{map[string]any{BlockFieldID: enID, BlockFieldTitle: "one'", BlockFieldTags: []any{"x", "y"}}},
	}}); err != nil {
		t.Fatalf("save of one partition: %v", err)
	}

	got := blockLists(t, load(t, s, id))
	if len(got["en"]) != 1 {
		t.Fatalf("en after partition save = %v, want 1 element", got["en"])
	}
	if gotID, _ := got["en"][0][BlockFieldID].(string); gotID != enID {
		t.Errorf("en id = %q, want %q — the row was replaced, not updated", gotID, enID)
	}
	if got["en"][0][BlockFieldTitle] != "one'" {
		t.Errorf("en title = %v, want one'", got["en"][0][BlockFieldTitle])
	}
	if tags, _ := got["en"][0][BlockFieldTags].([]any); len(tags) != 2 || tags[0] != "x" {
		t.Errorf("en tags = %v, want [x y] — the array column must survive the DO UPDATE path",
			got["en"][0][BlockFieldTags])
	}
	if len(got["ru"]) != 1 || got["ru"][0][BlockFieldTitle] != "раз" {
		t.Errorf("ru = %v — a key absent from the payload must not be touched", got["ru"])
	}
}

// keyedBlocksIDsSurvive: a reorder within a partition keeps the ids and
// rewrites the position, the central ChildRows invariant per key.
func keyedBlocksIDsSurvive(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldBlocks: map[string]any{
		"en": []any{
			map[string]any{BlockFieldTitle: "one"},
			map[string]any{BlockFieldTitle: "two"},
		},
	}})
	before := blockLists(t, load(t, s, id))["en"]
	if len(before) != 2 {
		t.Fatalf("seed produced %d en items, want 2", len(before))
	}
	id0, _ := before[0][BlockFieldID].(string)
	id1, _ := before[1][BlockFieldID].(string)

	if _, _, err := s.Save(ctx, &id, map[string]any{FieldBlocks: map[string]any{
		"en": []any{
			map[string]any{BlockFieldID: id1, BlockFieldTitle: "two"},
			map[string]any{BlockFieldID: id0, BlockFieldTitle: "one"},
		},
	}}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	after := blockLists(t, load(t, s, id))["en"]
	if len(after) != 2 {
		t.Fatalf("after reorder = %v, want 2 elements — rows were replaced or dropped", after)
	}
	if gotID, _ := after[0][BlockFieldID].(string); gotID != id1 {
		t.Errorf("after reorder = %v, want %q first — rows were replaced or position was not rewritten",
			after, id1)
	}
}

// keyedBlocksEmptyKey: an empty key means no partition; writing under it
// would be an unfiltered scope, a DELETE of every locale's rows at once.
func keyedBlocksEmptyKey(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldBlocks: map[string]any{
		"en": []any{map[string]any{BlockFieldTitle: "one"}},
	}})
	if _, _, err := s.Save(ctx, &id, map[string]any{FieldBlocks: map[string]any{
		"": []any{map[string]any{BlockFieldTitle: "ghost"}},
	}}); err != nil {
		t.Fatalf("save with an empty key: %v", err)
	}
	got := blockLists(t, load(t, s, id))
	if len(got) != 1 || len(got["en"]) != 1 || got["en"][0][BlockFieldTitle] != "one" {
		t.Errorf("%s = %v — an empty partition key must be ignored, not written or merged", FieldBlocks, got)
	}
}

// keyedBlocksGarbage: a non-list partition value leaves the partition alone;
// the user did not send an empty list, they sent garbage.
func keyedBlocksGarbage(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldBlocks: map[string]any{
		"en": []any{map[string]any{BlockFieldTitle: "one"}},
	}})
	if _, _, err := s.Save(ctx, &id, map[string]any{FieldBlocks: map[string]any{
		"en": "not a list",
	}}); err != nil {
		t.Fatalf("save with a garbage partition value: %v", err)
	}
	got := blockLists(t, load(t, s, id))
	if len(got["en"]) != 1 || got["en"][0][BlockFieldTitle] != "one" {
		t.Errorf("%s[en] = %v — a non-list partition value must leave the partition untouched",
			FieldBlocks, got["en"])
	}
}

// keyedBlocksEmptyList: an empty list is legal input and clears only its own
// partition.
func keyedBlocksEmptyList(t *testing.T, s Subject) {
	ctx := context.Background()
	id := seed(t, s, map[string]any{FieldTitle: "t", FieldBlocks: map[string]any{
		"en": []any{map[string]any{BlockFieldTitle: "one"}},
		"ru": []any{map[string]any{BlockFieldTitle: "раз"}},
	}})
	if _, _, err := s.Save(ctx, &id, map[string]any{FieldBlocks: map[string]any{
		"en": []any{},
	}}); err != nil {
		t.Fatalf("clearing one partition: %v", err)
	}
	got := blockLists(t, load(t, s, id))
	if len(got["en"]) != 0 {
		t.Errorf("%s[en] = %v, want empty", FieldBlocks, got["en"])
	}
	if len(got["ru"]) != 1 {
		t.Errorf("%s[ru] = %v — clearing en must not touch ru", FieldBlocks, got["ru"])
	}
}

// --- helpers ------------------------------------------------------------------

// seed creates the row and sends the remaining fields in a second Save: by the
// fixture contract create fills only the title, everything else goes through
// the update path.
func seed(t *testing.T, s Subject, fields map[string]any) string {
	t.Helper()
	ctx := context.Background()
	title, ok := fields[FieldTitle]
	if !ok {
		t.Fatalf("seed without %s: create requires it", FieldTitle)
	}
	id, ferr, err := s.Save(ctx, nil, map[string]any{FieldTitle: title})
	if err != nil || len(ferr) != 0 {
		t.Fatalf("seed create: err=%v ferr=%+v", err, ferr)
	}
	rest := make(map[string]any, len(fields))
	for k, v := range fields {
		if k != FieldTitle {
			rest[k] = v
		}
	}
	if len(rest) > 0 {
		if _, ferr, err := s.Save(ctx, &id, rest); err != nil || len(ferr) != 0 {
			t.Fatalf("seed update: err=%v ferr=%+v", err, ferr)
		}
	}
	return id
}

func load(t *testing.T, s Subject, id string) map[string]any {
	t.Helper()
	row, err := s.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if row == nil {
		t.Fatal("load returned no row")
	}
	return row
}

func ptr(s string) *string { return &s }
