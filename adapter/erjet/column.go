// Package erjet is the shipped storage for the editor: the persistence half
// of the "presentation <-> persistence" pair, on go-jet + pgx (Postgres).
//
// It pairs with package ui: ui says how a field looks, this package says how
// it is read and written. A cell is declared next to its field in one decl
// entry, so the two halves of one fact cannot drift apart.
//
// The dependency is one-way: this package imports the core, decl and ui, and
// the core cannot import it back - the reverse import would be a compile-time
// cycle. That is why the package lives beside the core rather than inside it:
// another backend is a sibling package that shares the core, ui and decl and
// nothing else, because go-jet builds Postgres SQL and pgtype is driver
// specific.
package erjet

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/qrotux/editrig-go/ui"
)

// Querier is the minimal pgx subset needed to read a row; *pgxpool.Pool and
// pgx.Tx both satisfy it.
type Querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// Column is one declaration cell pairing an editor field with a table column.
// It is the single source of truth for both reading and writing: SELECT takes
// proj, SET takes assign. A cell without assign physically cannot produce an
// assignment, which is what the entity's read-only contract rests on.
type Column struct {
	proj postgres.Projection
	// assign builds the assignment from a wire value (JSON-decoded: string/
	// bool/float64/nil). nil means the column is read-only.
	//
	// The second result is the same value in the wire shape of a read
	// (RFC 3339 for timestamptz, float64 for numeric): the audit diff compares
	// "before" from Row with "after" from here, and both sides must be
	// normalized identically, otherwise "...10:00:00.000Z" against
	// "...10:00:00Z" reports a false change.
	//
	// ok==false means the value did not parse (wrong type, unparseable date)
	// and there is no assignment. This is a defensive path: structural JSON
	// Schema validation runs earlier and normally catches it.
	assign func(v any) (postgres.ColumnAssigment, any, bool)

	// value is the same parse as assign, but the result is the expression
	// itself rather than an assignment. Needed where the value goes into
	// INSERT ... VALUES instead of SET: child table rows (child.go) have NOT
	// NULL columns without DEFAULT, and an expression cannot be recovered from
	// a postgres.ColumnAssigment.
	//
	// It is derived from the same parse as assign (see cell): written
	// separately, the two could disagree on what is a valid value, silently,
	// because both paths return ok=false the same way.
	value func(v any) (postgres.Expression, any, bool)

	// decode maps the scanned pgx value to its wire shape. nil means Normalize.
	decode func(v any) any

	// wire is the wire shape the cell accepts. decl.Lint reads it: without it
	// ui.List(...) next to erjet.Text lints clean and silently never writes.
	//
	// A field rather than a method: the cell is assembled from closures and
	// has no receiver type to derive the shape from, so only the constructor
	// can declare it.
	wire ui.Wire

	// load marks a satellite: the field's value lives in another table keyed
	// by the parent id. There is no column in this table and hence no proj;
	// Table.split takes such a cell out of the shared SELECT and it is read by
	// its own query.
	//
	// load without save (and vice versa) is impossible for a satellite: half a
	// pair would give a field that reads but silently does not write - the
	// form shows an input and Save does nothing.
	load func(ctx context.Context, q Querier, parentID string) (any, error)
	// save is the second write phase, on the same tx as the UPDATE of the
	// main table: atomicity of Save is a core contract and the second phase
	// must not commit on its own. Two different cells set it: a satellite
	// (load also non-nil, proj nil - the value lives entirely outside this
	// table) and Deferred (proj non-nil - the value belongs to its own column
	// but materializes only after the row is inserted). It returns the written
	// value in wire shape, exactly like the second result of assign and for
	// the same reason: both sides of the audit diff must be normalized alike.
	save func(ctx context.Context, tx pgx.Tx, parentID string, v any) (any, error)

	// satellite is set only by KeyedStrings: the column and the clearing
	// policy of this cell over its side table. nil for every other cell,
	// including the other satellites (ChildTable, Rels) - those already write
	// the whole row in one query and need no grouping.
	//
	// Store.Save reads it directly (not through save) to merge the upserts of
	// several cells of one side table into a single statement per
	// (parent, key): see KeyedTable.writeMany for why a per-column write
	// breaks on a NOT NULL neighbour.
	satellite *keyedSatellite
}

// Writable reports whether the cell accepts a write by either path: an
// assignment in the main table's SET (assign) or the satellite's own query
// (save).
//
// Both paths count here rather than in two predicates because decl.Lint asks
// exactly one question: "can the user write this field". Counting assign
// alone would make a satellite inexpressible: lint requires
// writable <=> !readonly, the field would have to be declared read-only, and
// the engine would then strip it from the payload before Save.
func (c Column) Writable() bool { return c.assign != nil || c.save != nil }

// IsSatellite reports whether the value lives outside this table (the cell
// has no projection); such cells are taken out of the shared SELECT and SET.
func (c Column) IsSatellite() bool { return c.proj == nil }

// Accepts reports whether the field's wire shape is one the cell can parse.
// Exported for decl.Cell: the gate must ask where the shape is declared, not
// a third list.
func (c Column) Accepts(w ui.Wire) bool { return c.wire.Match(w) }

// Frozen removes the write path (assign/save/value) from an assembled cell
// and keeps reading as is. For fields the storage can write but the client
// must not: the UI declares such a field .Readonly(), and Writable() must
// agree, or decl.Lint reports the mismatch.
func Frozen(c Column) Column {
	c.assign = nil
	c.save = nil
	c.value = nil
	return c
}

// --- cell constructors ---------------------------------------------------------

// ReadOnly is a read-only column: projection, no assign. wire is KindAny
// because the cell has no value/assign for the gate to check against; a zero
// Kind ("") would make the gate red on every read-only field.
func ReadOnly(c postgres.Projection) Column { return Column{proj: c, wire: ui.Wire{Kind: ui.KindAny}} }

// JSON is a read-only jsonb column. Its own decode: pgx returns jsonb as raw
// bytes and the form needs the parsed document. A broken or empty blob is
// returned as nil rather than an error: nothing to fix, the form renders a
// dash. wire is KindAny: the cell parses nothing, so the shape cannot be
// narrowed.
func JSON(c postgres.Projection) Column {
	return Column{proj: c, wire: ui.Wire{Kind: ui.KindAny}, decode: func(v any) any {
		var raw []byte
		switch x := v.(type) {
		case nil:
			return nil
		case []byte:
			raw = x
		case string:
			raw = []byte(x)
		default:
			return v // pgx already decoded the jsonb
		}
		if len(raw) == 0 {
			return nil
		}
		var out any
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil
		}
		return out
	}}
}

// JSONEditable is a jsonb column the user edits as a whole: the same decode
// as JSON plus writing the document as is.
//
// A separate constructor rather than a flag on JSON: the read-only contract
// rests on the absence of assign (Column.Writable), and a column able to play
// both roles would need a third marker somewhere else. Here the role is
// visible at the call site and decl.Lint checks it against .Readonly().
//
// No merge: the editor shows the whole document, so "what I see is what I
// save" is honest semantics. Partial edits of individual branches belong to a
// map cell, not this one.
//
// value and assign are derived from one expression (expr): written
// separately they could accept a document in UPDATE and reject it in the
// INSERT ... VALUES of a child row, silently - the defect cell and arrayCell
// avoid the same way.
func JSONEditable(c postgres.ColumnString) Column {
	col := JSON(c)
	expr := func(v any) (postgres.StringExpression, bool) {
		if v == nil {
			return postgres.StringExp(postgres.NULL), true
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, false // not serializable: skip, like every other cell
		}
		// The cast is mandatory: the parameter travels as text and the column
		// is jsonb - Postgres refuses the assignment without an explicit cast.
		return postgres.StringExp(postgres.CAST(postgres.String(string(raw))).AS("jsonb")), true
	}
	col.assign = func(v any) (postgres.ColumnAssigment, any, bool) {
		e, ok := expr(v)
		if !ok {
			return nil, nil, false
		}
		return c.SET(e), v, true
	}
	col.value = func(v any) (postgres.Expression, any, bool) {
		e, ok := expr(v)
		if !ok {
			return nil, nil, false
		}
		return e, v, true
	}
	return col
}

// --- value parsing: one parse, two outputs -------------------------------------

// cell is the shared scalar cell constructor: the wire value is parsed once
// (parse) and feeds two outputs - the expression (value, for INSERT ... VALUES
// of a child row) and the assignment (assign, for SET). Written separately
// they could disagree on what counts as a valid value, silently, because both
// paths return ok=false on rejected input.
//
// The generic works here and not in arrayCell (array.go) because of the
// nameability of SET's parameter: for arrays SET takes jet.Array[E], a type
// from go-jet's internal/ that cannot appear in a constraint. Here SET takes
// StringExpression/IntegerExpression/..., all exported; SET is a method, so
// it is passed as a bound method value (c.SET) typed func(E) ..., Go infers E
// from that argument, and parse is required to name the same E in its
// signature, so both really see one type.
func cell[E postgres.Expression](proj postgres.Projection, w ui.Wire,
	set func(E) postgres.ColumnAssigment,
	parse func(v any) (E, any, bool)) Column {
	return Column{
		proj: proj,
		wire: w,
		value: func(v any) (postgres.Expression, any, bool) {
			e, echo, ok := parse(v)
			if !ok {
				return nil, nil, false
			}
			return e, echo, true
		},
		assign: func(v any) (postgres.ColumnAssigment, any, bool) {
			e, echo, ok := parse(v)
			if !ok {
				return nil, nil, false
			}
			return set(e), echo, true
		},
	}
}

// Text is a nullable text/varchar column: explicit JSON null becomes SQL NULL.
func Text(c postgres.ColumnString) Column {
	return cell(c, ui.Wire{Kind: ui.KindString}, c.SET,
		func(v any) (postgres.StringExpression, any, bool) {
			switch x := v.(type) {
			case nil:
				return postgres.StringExp(postgres.NULL), nil, true
			case string:
				return postgres.String(x), x, true
			}
			return nil, nil, false
		})
}

// Str is a string column with NOT NULL semantics. A non-empty fallback
// replaces an empty string with the domain default; JSON null cannot be
// written here (under a partial UPDATE that simply means "do not write").
func Str(c postgres.ColumnString, fallback string) Column {
	return cell(c, ui.Wire{Kind: ui.KindString}, c.SET,
		func(v any) (postgres.StringExpression, any, bool) {
			s, ok := v.(string)
			if !ok {
				return nil, nil, false
			}
			if s == "" && fallback != "" {
				s = fallback
			}
			return postgres.String(s), s, true
		})
}

// Bool is a boolean column. NULL is never written: such columns have a
// DEFAULT and the form sends a plain boolean.
func Bool(c postgres.ColumnBool) Column {
	return cell(c, ui.Wire{Kind: ui.KindBool}, c.SET,
		func(v any) (postgres.BoolExpression, any, bool) {
			b, ok := v.(bool)
			if !ok {
				return nil, nil, false
			}
			return postgres.Bool(b), b, true
		})
}

// Num is a nullable numeric column fed from a JSON number; the value is not
// truncated to an integer.
func Num(c postgres.ColumnFloat) Column {
	return cell(c, ui.Wire{Kind: ui.KindNumber}, c.SET,
		func(v any) (postgres.FloatExpression, any, bool) {
			switch x := v.(type) {
			case nil:
				return postgres.FloatExp(postgres.NULL), nil, true
			case float64:
				return postgres.CAST(postgres.Float(x)).AS_NUMERIC(), x, true
			}
			return nil, nil, false
		})
}

// maxSafeInt is the bound of integers representable in float64 without losing
// digits.
//
// It is deliberately the cell's bound too: the wire is JSON, where a number is
// always float64, so a bigint above 2^53 would reach the form already
// distorted. Comparing with math.MaxInt64 would be a trap: MaxInt64 is not
// exactly representable in float64, and "x > math.MaxInt64" lets through
// values that overflow int64(x).
const maxSafeInt = float64(1 << 53)

// wholeNumber is the one "JSON number fits an integer column" check shared
// by the scalar cell and the array element, so the same value cannot be
// accepted in one place and rejected in the other.
//
// It returns both the parsed value and its wire echo: -0 must echo as 0, or
// the fmt.Sprint comparison of the audit diff reports a change on every Save.
func wholeNumber(f float64) (n int64, echo float64, ok bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) ||
		f > maxSafeInt || f < -maxSafeInt {
		return 0, 0, false
	}
	n = int64(f)
	return n, float64(n), true
}

// Int is a nullable integer column (int2/int4/int8 are all ColumnInteger in
// go-jet). Separate from Num because ColumnInteger and ColumnFloat are
// different interfaces with different method sets.
//
// A non-integer is not rounded: structural validation of "type": "integer"
// runs earlier and such a value normally never gets here. Rounding would
// write something other than what the form showed, with nobody noticing.
func Int(c postgres.ColumnInteger) Column {
	return cell(c, ui.Wire{Kind: ui.KindInteger}, c.SET,
		func(v any) (postgres.IntegerExpression, any, bool) {
			switch x := v.(type) {
			case nil:
				return postgres.IntExp(postgres.NULL), nil, true
			case float64:
				n, echo, ok := wholeNumber(x)
				if !ok {
					return nil, nil, false
				}
				// The echo is float64, not int64: "before" comes from
				// Normalize, which also maps integers to float64. Differing
				// types would make the audit diff report a change on every
				// Save.
				return postgres.Int64(n), echo, true
			}
			return nil, nil, false
		})
}

// UUID is a nullable uuid column fed from a JSON string. An empty string
// clears it: rjsf sends "" for an erased text input.
func UUID(c postgres.ColumnString) Column {
	return cell(c, ui.Wire{Kind: ui.KindString}, c.SET,
		func(v any) (postgres.StringExpression, any, bool) {
			switch x := v.(type) {
			case nil:
				return postgres.StringExp(postgres.NULL), nil, true
			case string:
				if x == "" {
					return postgres.StringExp(postgres.NULL), nil, true
				}
				return postgres.CAST(postgres.String(x)).AS_UUID(), x, true
			}
			return nil, nil, false
		})
}

// Time is a nullable timestamptz column fed from an RFC 3339 string (the
// shape Normalize produces on read and rjsf's DateTimeWidget sends back). An
// unparseable string is not written: structural validation does not assert
// formats.
func Time(c postgres.ColumnTimestampz) Column {
	return cell(c, ui.Wire{Kind: ui.KindString}, c.SET,
		func(v any) (postgres.TimestampzExpression, any, bool) {
			switch x := v.(type) {
			case nil:
				return postgres.TimestampzExp(postgres.NULL), nil, true
			case string:
				if x == "" {
					return postgres.TimestampzExp(postgres.NULL), nil, true
				}
				t, err := time.Parse(time.RFC3339, x)
				if err != nil {
					return nil, nil, false
				}
				return postgres.TimestampzT(t), Normalize(t), true
			}
			return nil, nil, false
		})
}

// Wall-clock layouts. Three, not one, and all three arrive from the same
// form: <input type="datetime-local"> does not return .value verbatim, it
// re-serializes it per the HTML spec -
//
//	set "2026-08-05T10:00:00" -> .value returns "2026-08-05T10:00"
//	set "2026-08-05T10:00:05" -> .value returns "2026-08-05T10:00:05.000"
//
// so the form sends the short form for zero seconds and the millisecond form
// otherwise. The canonical layout is the one the value travels and is stored
// in; the cell must accept the other two, or a value with non-zero seconds
// becomes unsaveable silently (assign returns ok=false, Table.Assignments
// skips the column, the form "saves" without writing anything).
const (
	localTimeLayout      = "2006-01-02T15:04:05"
	localTimeShortLayout = "2006-01-02T15:04"
	localTimeMilliLayout = "2006-01-02T15:04:05.000"
)

// TimeLocal is a nullable `timestamp without time zone` column treated as
// wall-clock: the digits mean exactly what is shown and neither end converts
// a zone.
//
// A separate cell from Time rather than a variant because the difference is
// semantic, not typed: Time deals with an instant (timestamptz, RFC 3339, UTC
// on the wire), this one with a wall clock that has no zone at all. A
// zoneless column that the cell decorates with "Z" lies about the data, and
// the only way to catch it is by eye, on a live form, in another time zone.
//
// The presentation-side pair is ui.LocalTimestamp() with a local datetime
// widget. rjsf's stock "datetime" widget does not fit: it does
// utcToLocal/localToUTC, exactly the conversion that must not happen here.
func TimeLocal(c postgres.ColumnTimestamp) Column {
	col := cell(c, ui.Wire{Kind: ui.KindString}, c.SET,
		func(v any) (postgres.TimestampExpression, any, bool) {
			switch x := v.(type) {
			case nil:
				return postgres.TimestampExp(postgres.NULL), nil, true
			case string:
				if x == "" {
					return postgres.TimestampExp(postgres.NULL), nil, true
				}
				t, err := parseLocalTime(x)
				if err != nil {
					return nil, nil, false
				}
				return postgres.TimestampT(t), t.Format(localTimeLayout), true
			}
			return nil, nil, false
		})
	// Own decode is mandatory: Normalize formats time.Time in UTC with a Z
	// suffix, and a wall clock has no zone at all.
	col.decode = func(v any) any {
		t, ok := v.(time.Time)
		if !ok {
			return v // nil and anything unexpected pass through
		}
		return t.Format(localTimeLayout)
	}
	return col
}

// parseLocalTime tries the canonical layout, then the two forms the browser
// sends (see the constants above). A string with a zone matches none of them:
// the extra characters make time.Parse fail, and the value is rejected rather
// than silently truncated to a different instant.
func parseLocalTime(s string) (time.Time, error) {
	for _, layout := range []string{localTimeLayout, localTimeShortLayout, localTimeMilliLayout} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("erjet: %q is not a wall-clock timestamp", s)
}

// dateLayout is "YYYY-MM-DD", exactly what <input type="date"> sends and
// ui.Date() declares as "format": "date".
const dateLayout = "2006-01-02"

// Date is a timestamptz column edited as a calendar date rather than an
// instant.
//
// The column holds only midnight - a date, not a moment. A datetime widget
// would lie about it (showing hours the data does not have) and force an
// answer about a time zone a date does not have.
//
// Read: the scanned timestamptz is formatted as "YYYY-MM-DD" by its own
// decode, because Normalize produces RFC 3339 with a zone.
//
// Write: "YYYY-MM-DD" is stored as UTC midnight of that day. An empty string
// becomes SQL NULL, symmetric with Time. An unparseable string is rejected
// the same way: assign returns ok=false and Table.Assignments skips the
// column.
func Date(c postgres.ColumnTimestampz) Column {
	col := cell(c, ui.Wire{Kind: ui.KindString}, c.SET,
		func(v any) (postgres.TimestampzExpression, any, bool) {
			switch x := v.(type) {
			case nil:
				return postgres.TimestampzExp(postgres.NULL), nil, true
			case string:
				if x == "" {
					return postgres.TimestampzExp(postgres.NULL), nil, true
				}
				t, err := time.Parse(dateLayout, x)
				if err != nil {
					return nil, nil, false
				}
				return postgres.TimestampzT(t.UTC()), t.Format(dateLayout), true
			}
			return nil, nil, false
		})
	col.decode = func(v any) any {
		t, ok := v.(time.Time)
		if !ok {
			return v // nil and anything unexpected pass through
		}
		return t.Format(dateLayout)
	}
	return col
}

// timeOfDayLayout is the wire shape of the TimeOfDay cell, the same one
// ui.TimeOfDay() declares: "HH:MM", no seconds, no zone.
const timeOfDayLayout = "15:04"

// TimeOfDay is a column of SQL type time with "HH:MM" on the wire. An empty
// string and nil become SQL NULL; an unparseable string is rejected with
// ok=false and the column is skipped, like every neighbouring cell.
//
// Own decode is mandatory: pgx returns a time column as pgtype.Time
// (microseconds since midnight), and Normalize has no branch for that type.
func TimeOfDay(c postgres.ColumnTime) Column {
	col := cell(c, ui.Wire{Kind: ui.KindString}, c.SET,
		func(v any) (postgres.TimeExpression, any, bool) {
			switch x := v.(type) {
			case nil:
				return postgres.TimeExp(postgres.NULL), nil, true
			case string:
				if x == "" {
					return postgres.TimeExp(postgres.NULL), nil, true
				}
				t, err := time.Parse(timeOfDayLayout, x)
				if err != nil {
					return nil, nil, false
				}
				return postgres.TimeT(t), t.Format(timeOfDayLayout), true
			}
			return nil, nil, false
		})
	col.decode = func(v any) any {
		t, ok := v.(pgtype.Time)
		if !ok {
			return v // nil and anything unexpected pass through
		}
		if !t.Valid {
			return nil
		}
		return fmt.Sprintf("%02d:%02d", t.Microseconds/3_600_000_000, (t.Microseconds%3_600_000_000)/60_000_000)
	}
	return col
}

// DeferredFunc is the second-phase write. parentID is always non-empty: on
// create it is the fresh id from the Create hook, on update the id from the
// URL.
type DeferredFunc func(ctx context.Context, tx pgx.Tx, parentID string, v any) (assign any, err error)

// Deferred is a cell written in the second phase that returns what to assign
// to its own column. It separates two properties that proj == nil alone
// conflates: "the value lives outside this table" and "the value cannot be
// written until the parent row exists". A media field is the second without
// the first: the column is its own, but the media id appears only after the
// owner's INSERT.
//
// An explicit null does not reach the deferred phase: clearing is an ordinary
// first-phase assignment. Otherwise the "clear" button in the form would
// silently do nothing - save would return nil, Store would continue, and the
// column would never be nulled.
func Deferred(c postgres.ColumnString, save DeferredFunc) Column {
	return Column{
		proj: c,
		// wire is KindAny: no value ever reaches this cell (see assign), so
		// there is nothing to parse.
		//
		// This cell does not fit inside an object element of a child table,
		// and erjet.ChildRows panics at declaration time rather than letting
		// it through (childElement in child.go): it has assign but no value,
		// so an existing element would save the field and a new one would
		// lose it. Silently skipping is not an option technically either:
		// upsertRowSQL calls nc.Column.value(v) and asserts nc.Column.proj to
		// postgres.Column without nil checks, so a softer gate would trade
		// the skip for a nil panic at runtime.
		wire: ui.Wire{Kind: ui.KindAny},
		assign: func(v any) (postgres.ColumnAssigment, any, bool) {
			if v != nil {
				// A non-empty value never gets here: Table.Assignments
				// diverts it earlier (nc.Column.save != nil && in[nc.Name]
				// != nil) and the second phase materializes it through
				// save/assignOf.
				return nil, nil, false
			}
			return c.SET(postgres.StringExp(postgres.NULL)), nil, true
		},
		save: func(ctx context.Context, tx pgx.Tx, parentID string, v any) (any, error) {
			return save(ctx, tx, parentID, v)
		},
	}
}

// assignOf assigns the cell's own column a ready value (the result of save).
// The second phase needs exactly this: an ordinary cell's assign parses a
// wire value, whereas here the value is already domain-shaped.
//
// The cast is mandatory and lives inside the cell: the target is a uuid
// column exposed as postgres.ColumnString, and every uuid path casts
// explicitly (see UUID). Without it the statement is "SET col = $n" with a
// text parameter, which fails only against a live database.
func (c Column) assignOf(v any) postgres.ColumnAssigment {
	col := c.proj.(postgres.ColumnString)
	s, _ := v.(string)
	return col.SET(postgres.CAST(postgres.String(s)).AS_UUID())
}
