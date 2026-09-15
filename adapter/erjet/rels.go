package erjet

import (
	"context"

	"github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5"

	"github.com/qrotux/editrig-go/ui"
)

// RelsTable is a side table of relations: one row per (parent, target) pair,
// with the relation name in its own column (Path) and the position in Order.
//
// A storage shape, not a schema convention (the same boundary as KeyedTable):
// that a parent may have several kinds of relation, that they differ by a
// string in a column and that the target sits in its own column per
// collection is the shape. Which values Path takes and what the collections
// are called is known only to the application.
//
// ParentType/TargetType are SQL type names for an explicit cast of the
// parameters: typically uuid, while the parameter travels as text, and
// without the cast Postgres refuses the insert - only against a live
// database. Empty means no cast.
//
// ID is the surrogate PK, needed only as the secondary sort key: in existing
// data "order" can be NULL, and without it the order of such rows would
// flicker between queries. Nil means sorting by Order alone.
type RelsTable struct {
	Src postgres.Table
	// Parent is the string or integer foreign key to the owning row.
	Parent     postgres.Column
	Path       postgres.ColumnString
	Order      postgres.ColumnInteger
	ID         postgres.ColumnInteger
	ParentType string
	TargetType string
}

// Rels is an "ordered list of ids" satellite cell over a relations table.
// target accepts string and integer identifier columns.
//
// On the wire the field is an array of strings `["id1","id2"]` whose order is
// significant: it is the Order column. So reading sorts explicitly and
// writing assigns positions - otherwise dragging chips in the form would not
// persist, and the list would arrive in a new order every time.
//
// Writing is a replacement (DELETE by (parent, path) + INSERT by position),
// not an add/remove diff. With significant order a diff degenerates into
// rewriting Order on every row anyway, and the PK is surrogate and referenced
// by nothing: the cost is sequence consumption, the gain is that a
// "half reordered" state cannot exist.
//
// Several cells over one relations table is a normal configuration: each
// works strictly within its own path and never sees the others' rows.
func Rels(rt RelsTable, path string, target postgres.Column) Column {
	return Column{
		wire: ui.Wire{Kind: ui.KindList, Elem: ui.KindString},
		load: func(ctx context.Context, q Querier, parentID string) (any, error) {
			return rt.read(ctx, q, path, target, parentID)
		},
		save: func(ctx context.Context, tx pgx.Tx, parentID string, v any) (any, error) {
			ids, ok := idSlice(v)
			if !ok {
				// Not an array means the value did not parse and there is
				// nothing to write - same decision as ok==false in assign:
				// without it a string payload would erase every relation
				// with one DELETE.
				return nil, nil
			}
			if err := rt.replace(ctx, tx, path, target, parentID, ids); err != nil {
				return nil, err
			}
			// Return what was written in the same wire shape read produces:
			// the audit diff compares "before" with "after" via fmt.Sprint,
			// which for a slice is order-sensitive - exactly as needed.
			return anySlice(ids), nil
		},
	}
}

// read returns the relation targets of one parent, in Order.
//
// Rows of other paths are filtered twice: by the Path column and by "target
// IS NOT NULL". The second condition is not redundant: a row has exactly one
// target column filled, and a query by another path would return a column of
// NULLs.
func (rt RelsTable) read(ctx context.Context, q Querier, path string, target postgres.Column, parentID string) (any, error) {
	stmt := postgres.
		SELECT(target).
		FROM(rt.Src).
		WHERE(rt.parentEq(parentID).
			AND(rt.Path.EQ(postgres.String(path))).
			AND(target.IS_NOT_NULL()))

	if rt.ID != nil {
		stmt = stmt.ORDER_BY(rt.Order.ASC(), rt.ID.ASC())
	} else {
		stmt = stmt.ORDER_BY(rt.Order.ASC())
	}

	sql, args := stmt.Sql()
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// An empty array, not nil: the form field is a list, and null would reach
	// the widget instead of an empty list.
	out := []any{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		if len(vals) != 1 {
			continue
		}
		if id := normalizeIdentifier(vals[0]); id != "" {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// replace rewrites the whole relation list of this path.
//
// Inserts go one statement per element: lists here are dozens of rows, and
// one query per element reads well in the log and needs no hand-built VALUES.
func (rt RelsTable) replace(ctx context.Context, tx pgx.Tx, path string, target postgres.Column, parentID string, ids []string) error {
	del, args := rt.Src.DELETE().
		WHERE(rt.parentEq(parentID).AND(rt.Path.EQ(postgres.String(path)))).
		Sql()
	if _, err := tx.Exec(ctx, del, args...); err != nil {
		return err
	}

	for i, id := range ids {
		sql, args := rt.Src.
			INSERT(rt.Parent, rt.Path, target, rt.Order).
			VALUES(
				castString(parentID, rt.ParentType),
				postgres.String(path),
				castString(id, rt.TargetType),
				postgres.Int(int64(i)), // the array position is the order
			).
			Sql()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return err
		}
	}
	return nil
}

func (rt RelsTable) parentEq(parentID string) postgres.BoolExpression {
	// Comparison via ::text, as in Table.where: the query tolerates any
	// garbage in the id and returns "no rows" instead of failing on the cast.
	return postgres.CAST(rt.Parent).AS_TEXT().EQ(postgres.String(parentID))
}

// idSlice maps a wire value to a list of ids. Accepts both []any (JSON
// decode) and []string (a call from code).
//
// The cell removes duplicates and non-strings itself: uniqueItems is a schema
// guarantee, but the cell is also called past structural validation (tests,
// an importer), and a duplicate in the relations table is inexpressible in
// the form afterwards - it would show one chip and Save would silently drop
// the second.
func idSlice(v any) ([]string, bool) {
	var raw []any
	switch x := v.(type) {
	case []any:
		raw = x
	case []string:
		out := make([]string, 0, len(x))
		seen := make(map[string]bool, len(x))
		for _, s := range x {
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
		return out, true
	default:
		return nil, false
	}

	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		if !ok || s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out, true
}

func anySlice(ids []string) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}
