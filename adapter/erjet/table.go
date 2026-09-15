package erjet

import (
	"context"
	"fmt"

	"github.com/go-jet/jet/v2/postgres"

	"github.com/qrotux/editrig-go/decl"
)

// --- table ---------------------------------------------------------------------

// Named pairs an editor field name with its column. The order of Table.Cols
// is significant: it sets both the SELECT projection order and the SET order.
type Named struct {
	Name   string
	Column Column
}

// Table is the ordered column set of one table plus its primary key.
type Table struct {
	Src postgres.Table
	// ID is a string or integer PK column. Comparison goes via ::text: the query
	// tolerates any garbage in the path parameter and returns "no rows" (404),
	// whereas a cast to the PK type can fail it (500).
	ID   postgres.Column
	Cols []Named
}

// NewTable is the persistent projection of a declaration, from which SELECT
// and the partial UPDATE are built. id accepts string and integer columns.
//
// It lives here because the declaration is generic over the cell type and
// knows nothing about Postgres (an entity on another store writes the same
// declaration), while a store implementation may know about decl.
func NewTable(d decl.Set[Column], src postgres.Table, id postgres.Column) Table {
	cols := make([]Named, len(d))
	for i, f := range d {
		cols[i] = Named{Name: f.Name, Column: f.Col}
	}
	return Table{Src: src, ID: id, Cols: cols}
}

// Column returns the cell of a field by name.
func (t Table) Column(name string) (Column, bool) {
	for _, c := range t.Cols {
		if c.Name == name {
			return c.Column, true
		}
	}
	return Column{}, false
}

// Order returns the field names in declared order; the entity takes ui:order
// from it, so no separate order list exists.
func (t Table) Order() []string {
	out := make([]string, len(t.Cols))
	for i, c := range t.Cols {
		out[i] = c.Name
	}
	return out
}

// idField returns the name of the field whose cell projects onto the table's
// PK, and whether such a field is declared at all. The object projection of a
// child table (ChildRows) needs it: the element id travels as an object field,
// and only the declaration knows which one. A hard-coded "id" would silently
// stop working for a declaration that names the field differently, and the
// consequence (every element looks new, old rows are deleted, the cascade
// takes their translations) would look like a successful list round-trip.
//
// Compared by name and table rather than interface identity: a column rebuilt
// by postgres.StringColumn("id") is the same column for SQL but a different
// interface value, and == would report "no such field" for no reason.
func (t Table) idField() (string, bool) {
	for _, nc := range t.Cols {
		col, ok := nc.Column.proj.(postgres.Column)
		if !ok {
			continue // a satellite or an expression projection is never the PK
		}
		if sameColumn(col, t.ID) {
			return nc.Name, true
		}
	}
	return "", false
}

// sameColumn reports whether a and b are the same column in SQL terms.
func sameColumn(a, b postgres.Column) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Name() == b.Name() && a.TableName() == b.TableName()
}

func (t Table) where(id string) postgres.BoolExpression {
	return postgres.CAST(t.ID).AS_TEXT().EQ(postgres.String(id))
}

// split separates keys into column-backed and satellite ones, preserving the
// order of each half: SELECT projections come from the first, the order of
// satellite queries from the second, and neither may flicker between calls.
//
// It walks the keys rather than the declaration because the caller reads a
// subset (the audit reads only writable fields), and an extra column in the
// SELECT would shift the value-to-key mapping when scanning.
func (t Table) split(keys []string) (cols, sats []string) {
	cols = make([]string, 0, len(keys))
	for _, k := range keys {
		if c, ok := t.Column(k); ok && c.IsSatellite() {
			sats = append(sats, k)
			continue
		}
		cols = append(cols, k)
	}
	return cols, sats
}

// RowSQL builds the SELECT of the listed fields of one row. Keys must be
// column-backed: a satellite has no projection and is read by its own query
// (see Row).
func (t Table) RowSQL(keys []string, id string) (string, []any) {
	projs := t.projsOf(keys)
	return postgres.SELECT(projs[0], projs[1:]...).
		FROM(t.Src).
		WHERE(t.where(id)).
		Sql()
}

// projsOf returns the projections of the listed fields in the same order. One
// for everyone building a SELECT over a subset of the declaration (RowSQL
// here, readRows in child.go): disagreeing on what counts as a projectable
// field, they would scan values into the wrong keys.
//
// A satellite panics rather than being skipped: silently dropping a key
// would shift the value-to-key mapping. Same decision as UpdateSQL on an
// empty SET; the normal path calls split earlier in the stack.
func (t Table) projsOf(keys []string) []postgres.Projection {
	projs := make([]postgres.Projection, 0, len(keys))
	for _, k := range keys {
		c, _ := t.Column(k)
		if c.IsSatellite() {
			panic("erjet: projection requested for satellite key " + k + " — caller must split first")
		}
		projs = append(projs, c.proj)
	}
	return projs
}

// Row reads the listed fields of one row; (nil, nil) means no row. Works from
// a pool and from a transaction alike.
//
// Satellite fields are read here too, by their own queries: the method already
// has the Querier and the parent id, all they need. So neither Store.Load nor
// the "before" read preceding an UPDATE knows about satellites.
//
// The price is N+1, one query per satellite field, accepted deliberately:
// satellites are few, and the alternative (LATERAL subqueries in the shared
// SELECT) would make a cell build a fragment of someone else's query.
func (t Table) Row(ctx context.Context, q Querier, keys []string, id string) (map[string]any, error) {
	cols, sats := t.split(keys)
	if len(sats) == 0 {
		return t.rowCols(ctx, q, cols, id)
	}

	var out map[string]any
	if len(cols) > 0 {
		row, err := t.rowCols(ctx, q, cols, id)
		if err != nil || row == nil {
			return nil, err // (nil, nil): no row
		}
		out = row
	} else {
		// Every requested field is a satellite: nothing to read from the
		// main table, but "no row" must still be told from "row with no
		// values", or a Save of a single satellite field would write into a
		// non-existent parent and Load would return an empty object instead
		// of 404.
		ok, err := t.Exists(ctx, q, id)
		if err != nil || !ok {
			return nil, err
		}
		out = make(map[string]any, len(sats))
	}

	for _, k := range sats {
		c, _ := t.Column(k)
		v, err := c.load(ctx, q, id)
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

// rowCols reads the column-backed fields with one SELECT.
func (t Table) rowCols(ctx context.Context, q Querier, keys []string, id string) (map[string]any, error) {
	sql, args := t.RowSQL(keys, id)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	vals, err := rows.Values()
	if err != nil {
		return nil, err
	}
	if len(vals) != len(keys) {
		return nil, fmt.Errorf("erjet: %d values for %d keys", len(vals), len(keys))
	}
	out := make(map[string]any, len(keys))
	for i, k := range keys {
		c, _ := t.Column(k)
		if col, ok := c.proj.(postgres.Column); ok && sameColumn(col, t.ID) {
			out[k] = normalizeIdentifier(vals[i])
			continue
		}
		if c.decode != nil {
			out[k] = c.decode(vals[i])
			continue
		}
		out[k] = Normalize(vals[i])
	}
	return out, rows.Err()
}

// Assignments intersects the payload with the writable columns: one
// assignment per editable key that arrived. A key absent from the declaration
// or whose cell has no assign cannot produce one - this is the choke point of
// the read-only contract.
//
// It walks the declaration, not the map: SET must be deterministic (stable
// SQL means a working statement cache and a readable log).
//
// changed lists the written keys in the same order; newVals holds their
// wire-shaped values for the audit diff.
func (t Table) Assignments(in map[string]any) (assigns []postgres.ColumnAssigment, changed []string, newVals map[string]any) {
	assigns = make([]postgres.ColumnAssigment, 0, len(in))
	changed = make([]string, 0, len(in))
	newVals = make(map[string]any, len(in))
	for _, nc := range t.Cols {
		// A satellite has no assign at all (rels.go, satellite.go): without
		// this branch Writable() would lead to a nil function call on every
		// Save whose payload carries the field, explicit null included. Its
		// other half is Deferred.
		// A column-backed deferred cell is written by the second phase if it
		// has something to materialize; an explicit null is clearing and must
		// stay here.
		if !nc.Column.Writable() || nc.Column.IsSatellite() ||
			(nc.Column.save != nil && in[nc.Name] != nil) {
			continue
		}
		v, present := in[nc.Name]
		if !present {
			continue
		}
		assign, normalized, ok := nc.Column.assign(v)
		if !ok {
			continue
		}
		assigns = append(assigns, assign)
		changed = append(changed, nc.Name)
		newVals[nc.Name] = normalized
	}
	return assigns, changed, newVals
}

// Deferred returns the cells that write themselves with their own queries in
// the same transaction: satellites (value in another table) and column-backed
// deferred cells (value depends on an already existing parent).
func (t Table) Deferred(in map[string]any) []Named {
	out := make([]Named, 0, 2)
	for _, nc := range t.Cols {
		if nc.Column.save == nil {
			continue
		}
		v, present := in[nc.Name]
		if !present {
			continue // "not sent means do not touch", the same rule as for columns
		}
		// An explicit null goes to the first phase only for a column-backed
		// cell, where assign accepts it and nulls the column. A satellite has
		// assign == nil and the first phase always skips it, so skipping nil
		// here too would send {"bio": null} nowhere and clearing a
		// translation would silently stop working (store.go passes nil
		// straight to save).
		if v == nil && !nc.Column.IsSatellite() {
			continue
		}
		out = append(out, nc)
	}
	return out
}

// UpdateSQL builds the partial UPDATE: SET holds only the passed assignments
// plus touch = now() when named (row bookkeeping, not from the payload). Empty
// assigns is a caller error: UPDATE without SET is syntactically invalid, and
// the caller must return early.
func (t Table) UpdateSQL(assigns []postgres.ColumnAssigment, touch postgres.ColumnTimestampz, id string) (string, []any) {
	if len(assigns) == 0 {
		panic("erjet: UpdateSQL with no assignments — caller must return early")
	}
	all := make([]any, 0, len(assigns))
	for _, a := range assigns[1:] {
		all = append(all, a)
	}
	if touch != nil {
		all = append(all, touch.SET(postgres.NOW()))
	}
	return t.Src.UPDATE().
		SET(assigns[0], all...).
		WHERE(t.where(id)).
		Sql()
}

// DeleteSQL builds the delete of one row by id. A method rather than a
// literal in the entity so that the ::text PK comparison (see where) lives in
// one place with SELECT and UPDATE: drifting apart, they would give 500
// instead of 404 on a garbage id.
func (t Table) DeleteSQL(id string) (string, []any) {
	return t.Src.DELETE().WHERE(t.where(id)).Sql()
}

// Exists reports whether a row with this id exists, for callers that need
// only "404 or not" without reading the whole row.
func (t Table) Exists(ctx context.Context, q Querier, id string) (bool, error) {
	sql, args := postgres.SELECT(
		// Project the PK, not a literal: postgres.Int(1) would travel as
		// parameter $1 and shift the id to $2, and pgx fails encoding a number
		// as text.
		postgres.EXISTS(postgres.SELECT(t.ID).FROM(t.Src).WHERE(t.where(id))),
	).Sql()
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, rows.Err()
	}
	vals, err := rows.Values()
	if err != nil {
		return false, err
	}
	if len(vals) != 1 {
		return false, fmt.Errorf("erjet: exists returned %d values", len(vals))
	}
	found, _ := vals[0].(bool)
	return found, rows.Err()
}

// TakenByOther reports whether another row holds this value in column col.
// The comparison is case-sensitive (`=`) and the own row is excluded by PK -
// the uniqueness gate shape the application expects.
//
// An empty selfID means no self-exclusion: comparing id::text with an empty
// string matches no real uuid, which is exactly right on create (self does
// not exist yet).
func (t Table) TakenByOther(ctx context.Context, q Querier, col postgres.ColumnString, value, selfID string) (bool, error) {
	sql, args := postgres.SELECT(
		postgres.EXISTS(postgres.SELECT(t.ID).FROM(t.Src).WHERE(
			col.EQ(postgres.String(value)).AND(
				postgres.CAST(t.ID).AS_TEXT().NOT_EQ(postgres.String(selfID)),
			),
		)),
	).Sql()
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, rows.Err()
	}
	vals, err := rows.Values()
	if err != nil {
		return false, err
	}
	if len(vals) != 1 {
		return false, fmt.Errorf("erjet: exists returned %d values", len(vals))
	}
	taken, _ := vals[0].(bool)
	return taken, rows.Err()
}
