package erjet

import (
	"time"

	"github.com/go-jet/jet/v2/postgres"

	"github.com/qrotux/editrig-go/ui"
)

// Array cells: one column value is an ordered list of scalars.
//
// Why the core is closures rather than a generic over the column type: go-jet
// declares the column method as SET(jet.Array[E]), and jet.Array lives in
// internal/, unreachable from outside. A constraint in terms of
// postgres.Array[E] does not satisfy it - postgres.Array is a defined type,
// not an alias, and interface implementation requires exact parameter types.
// The compiler says:
//
//	have SET(jet.Array[postgres.StringExpression]) jet.ColumnAssigment
//	want SET(postgres.Array[postgres.StringExpression]) postgres.ColumnAssigment
//
// So the types are erased by closures: the shared list arithmetic lives in one
// arrayCell, and a per-element-type constructor does exactly two things -
// parses an element and picks the literal.
//
// Not to be confused with Rels (rels.go): there the list is ids of other rows
// in a side table, here it is values in the cell's own column. Hence the
// behavioural difference: Rels deduplicates (two equal ids are one relation),
// an array never does (two equal values are two elements).
//
// There is deliberately no TimeLocals (a wall-clock array): no schema has
// needed it, and an unverifiable constructor is worse than none.

// arrayCell is the shared part of every array cell.
//
// parse receives the payload elements and returns the array expression
// together with the assignment and the wire echo; null is the same pair for
// SQL NULL. The core routes them to the two write halves - value for the
// INSERT ... VALUES of a child row and assign for SET - by the same "one
// parse, two outputs" trade as the scalar cell (column.go): diverging, the
// halves would give a value accepted by UPDATE and silently rejected by
// INSERT.
//
// The (expression, assignment) pair cannot be split into "expression plus a
// SET wrapper in the core": c.SET takes jet.Array[E], unnameable outside
// go-jet (see the file header), so only the constructor's closure, which
// knows the concrete E, can wrap the expression into an assignment. The echo
// is still built by the constructor: for Timestamps the input and the read
// shape differ, and Normalize over an already wire-shaped value is a no-op.
func arrayCell(
	proj postgres.Projection,
	elem ui.Kind,
	parse func(vals []any) (postgres.Expression, postgres.ColumnAssigment, []any, bool),
	null func() (postgres.Expression, postgres.ColumnAssigment),
) Column {
	return Column{
		proj:   proj,
		wire:   ui.Wire{Kind: ui.KindList, Elem: elem},
		decode: decodeArray,
		value: func(v any) (postgres.Expression, any, bool) {
			switch x := v.(type) {
			case nil:
				e, _ := null()
				return e, nil, true
			case []any:
				e, _, echo, ok := parse(x)
				if !ok {
					return nil, nil, false
				}
				return e, echo, true
			}
			return nil, nil, false
		},
		assign: func(v any) (postgres.ColumnAssigment, any, bool) {
			switch x := v.(type) {
			case nil:
				_, as := null()
				return as, nil, true
			case []any:
				_, as, echo, ok := parse(x)
				if !ok {
					return nil, nil, false
				}
				return as, echo, true
			}
			// Not a list means the value did not parse. Same decision as the
			// scalar cells: without it a string payload would overwrite the
			// column with a single element.
			return nil, nil, false
		},
	}
}

// decodeArray maps a pgx slice to []any with Normalize applied per element.
//
// pgx decodes any Postgres array into []any through ArrayCodec.DecodeValue
// (each element already decoded by its element codec: int32, string,
// float64, time.Time, pgtype.Numeric) - typed slices like []int32 never
// arrive. The cell's job is to run each element through Normalize so the read
// shape matches the echo assign returns.
func decodeArray(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		return normalizeEach(x)
	default:
		return v
	}
}

func normalizeEach(in []any) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = Normalize(v)
	}
	return out
}

// --- constructors per element type ---------------------------------------------

// Strings is a text[] cell. varchar[] should work with the same cell under
// Postgres assignment cast rules, but the live conformance gate
// (storetest.RunExtraTypes) exercises only text[], and parameter encoding is
// exactly what a unit test cannot see - so "should" is inferred, not
// verified.
func Strings(c postgres.ColumnStringArray) Column {
	return arrayCell(c, ui.KindString,
		func(vals []any) (postgres.Expression, postgres.ColumnAssigment, []any, bool) {
			out := make([]string, 0, len(vals))
			echo := make([]any, 0, len(vals))
			for _, v := range vals {
				s, ok := v.(string)
				if !ok {
					return nil, nil, nil, false
				}
				out = append(out, s)
				echo = append(echo, s)
			}
			arr := postgres.StringArray(out...)
			return arr, c.SET(arr), echo, true
		},
		func() (postgres.Expression, postgres.ColumnAssigment) {
			n := postgres.ArrayExp[postgres.StringExpression](postgres.NULL)
			return n, c.SET(n)
		})
}

// Ints is an int4[] cell. int2[]/int8[] should work with the same cell under
// Postgres assignment cast rules, but the live gate (storetest.RunExtraTypes)
// exercises only int4[] - see the caveat on Strings. A non-integer element
// rejects the whole write, as in Int.
func Ints(c postgres.ColumnIntegerArray) Column {
	return arrayCell(c, ui.KindInteger,
		func(vals []any) (postgres.Expression, postgres.ColumnAssigment, []any, bool) {
			out := make([]int64, 0, len(vals))
			echo := make([]any, 0, len(vals))
			for _, v := range vals {
				f, ok := v.(float64)
				if !ok {
					return nil, nil, nil, false
				}
				n, e, ok := wholeNumber(f)
				if !ok {
					return nil, nil, nil, false
				}
				out = append(out, n)
				echo = append(echo, e)
			}
			arr := postgres.Int64Array(out...)
			return arr, c.SET(arr), echo, true
		},
		func() (postgres.Expression, postgres.ColumnAssigment) {
			n := postgres.ArrayExp[postgres.IntegerExpression](postgres.NULL)
			return n, c.SET(n)
		})
}

// Floats is a float8[] cell. float4[]/numeric[] should work with the same
// cell under Postgres assignment cast rules, but the live gate
// (storetest.RunExtraTypes) exercises only float8[] - see the caveat on
// Strings.
func Floats(c postgres.ColumnFloatArray) Column {
	return arrayCell(c, ui.KindNumber,
		func(vals []any) (postgres.Expression, postgres.ColumnAssigment, []any, bool) {
			out := make([]float64, 0, len(vals))
			echo := make([]any, 0, len(vals))
			for _, v := range vals {
				f, ok := v.(float64)
				if !ok {
					return nil, nil, nil, false
				}
				out = append(out, f)
				echo = append(echo, f)
			}
			arr := postgres.Float64Array(out...)
			return arr, c.SET(arr), echo, true
		},
		func() (postgres.Expression, postgres.ColumnAssigment) {
			n := postgres.ArrayExp[postgres.FloatExpression](postgres.NULL)
			return n, c.SET(n)
		})
}

// Timestamps is a timestamptz[] cell: RFC 3339 elements, as in the scalar
// Time.
//
// The echo runs every element through Normalize, and that is not cosmetic: a
// read returns []time.Time, hence UTC strings with "Z", while the input may
// carry any offset. Without it the two sides of the audit diff drift apart.
func Timestamps(c postgres.ColumnTimestampzArray) Column {
	return arrayCell(c, ui.KindString,
		func(vals []any) (postgres.Expression, postgres.ColumnAssigment, []any, bool) {
			out := make([]time.Time, 0, len(vals))
			echo := make([]any, 0, len(vals))
			for _, v := range vals {
				s, ok := v.(string)
				if !ok {
					return nil, nil, nil, false
				}
				t, err := time.Parse(time.RFC3339, s)
				if err != nil {
					return nil, nil, nil, false
				}
				out = append(out, t)
				echo = append(echo, Normalize(t))
			}
			arr := postgres.TimestampzArray(out...)
			return arr, c.SET(arr), echo, true
		},
		func() (postgres.Expression, postgres.ColumnAssigment) {
			n := postgres.ArrayExp[postgres.TimestampzExpression](postgres.NULL)
			return n, c.SET(n)
		})
}
