package erjet

import (
	"context"
	"sort"

	"github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5"

	"github.com/qrotux/editrig-go/ui"
)

// KeyedTable is a side table storing one form field as rows, one per
// (parent, key) pair. It is a storage shape, not a schema convention: only the
// caller knows what the columns are called and what the key means.
//
// Translations live this way (`*_locales`, key = locale), and so would any map
// field - per-device settings, per-plan limits - which is why nothing here
// mentions locales: the entity declares the conventions of its own schema.
//
// ParentType/KeyType are SQL type names for an explicit cast of the
// parameters; empty means no cast. The key is often an enum and the parent a
// uuid: without the cast Postgres receives text and fails ("column is of type
// ... but expression is of type text"), and only against a live database.
type KeyedTable struct {
	Src postgres.Table
	// Parent is the string or integer foreign key to the owning row.
	Parent     postgres.Column
	Key        postgres.ColumnString
	ParentType string
	KeyType    string
}

// ClearPolicy is how a cell erases a value: by nulling its own column or by
// deleting the whole row.
//
// The choice is mandatory (third argument of KeyedStrings, no default) because
// the cost of a mistake is asymmetric: a side table often holds several
// fields, and deleting the row to clear one field silently takes the
// neighbours with it. The opposite mistake is cheaper - a row of empty values
// is merely garbage.
type ClearPolicy struct {
	deleteRow bool
	// value is what to write into the column instead of the value; nil means
	// NULL.
	value postgres.StringExpression
}

// ClearSetNull clears by nulling the column (UPDATE ... SET col = NULL) and
// keeps the row: the only safe option when the table holds anything besides
// this field.
func ClearSetNull() ClearPolicy { return ClearPolicy{} }

// ClearSet clears by writing the given value instead of NULL, for columns with
// NOT NULL or a domain empty value (postgres.String("")).
func ClearSet(empty postgres.StringExpression) ClearPolicy {
	return ClearPolicy{value: empty}
}

// ClearDeleteRow clears by deleting the whole (parent, key) row.
//
// Fits only when the side table holds exactly this field: the delete takes
// every other column of the row with it, including those a neighbouring cell
// edits.
func ClearDeleteRow() ClearPolicy { return ClearPolicy{deleteRow: true} }

// KeyedStrings is a "key -> string" satellite cell over a side table.
//
// On the wire the field is an object `{"en": "...", "ru": "..."}`: one form
// field, not one per key, so it has one place in ui:order, one section and
// one label. Keys without a row are absent from the map - which of them to
// show empty is the schema's decision (it knows the active keys, the storage
// does not).
//
// Several cells over one side table is a legitimate configuration (two
// localized fields side by side): each inserts its own column and the
// (parent, key) conflict updates only its own. What clearing does is decided
// by clear.
func KeyedStrings(kt KeyedTable, col postgres.ColumnString, clear ClearPolicy) Column {
	return Column{
		wire: ui.Wire{Kind: ui.KindMap, Elem: ui.KindString},
		load: func(ctx context.Context, q Querier, parentID string) (any, error) {
			return kt.read(ctx, q, col, parentID)
		},
		save: func(ctx context.Context, tx pgx.Tx, parentID string, v any) (any, error) {
			patch, ok := v.(map[string]any)
			if !ok {
				// Not a map means the value did not parse and there is nothing
				// to write - same decision as ok==false in assign: structural
				// validation runs earlier, this path is defensive.
				return nil, nil
			}
			if err := kt.write(ctx, tx, col, parentID, patch, clear); err != nil {
				return nil, err
			}
			// Re-read after the write, in the same transaction: the audit diff
			// compares the full "before" with the full "after", and returning
			// only the patch would tell the journal the other keys vanished.
			return kt.read(ctx, tx, col, parentID)
		},
		// satellite gives Store.Save a path around save: several KeyedStrings
		// cells over one table are grouped and written by one upsert
		// (writeKeyedGroups), while save above stays functional on its own
		// for any call that bypasses the grouping.
		satellite: &keyedSatellite{table: kt, col: col, clear: clear},
	}
}

// keyedSatellite is the identity of a KeyedStrings cell needed only for
// grouping: which side table, which column, which clearing policy.
type keyedSatellite struct {
	table KeyedTable
	col   postgres.ColumnString
	clear ClearPolicy
}

// writeKeyedGroups groups the deferred KeyedStrings cells of one Save by side
// table and writes each group with one writeMany. It returns the names of the
// cells it wrote, for which Store.Save reads the result directly (kt.read)
// instead of calling their save again.
//
// Cells without satellite (Deferred, the other satellites) and entries whose
// payload is not a map are left to the ordinary Store.Save path
// (nc.Column.save).
func writeKeyedGroups(ctx context.Context, tx pgx.Tx, parentID string, deferred []Named, in map[string]any) (map[string]bool, error) {
	type group struct {
		table  KeyedTable
		writes []keyedColumnWrite
	}
	order := make([]string, 0)
	groups := map[string]*group{}
	handled := map[string]bool{}

	for _, nc := range deferred {
		sc := nc.Column.satellite
		if sc == nil {
			continue
		}
		patch, ok := in[nc.Name].(map[string]any)
		if !ok {
			continue
		}
		key := sc.table.groupKey()
		g, exists := groups[key]
		if !exists {
			g = &group{table: sc.table}
			groups[key] = g
			order = append(order, key)
		}
		g.writes = append(g.writes, keyedColumnWrite{col: sc.col, clear: sc.clear, patch: patch})
		handled[nc.Name] = true
	}

	for _, key := range order {
		g := groups[key]
		if err := g.table.writeMany(ctx, tx, parentID, g.writes); err != nil {
			return nil, err
		}
	}
	return handled, nil
}

// groupKey is the side table's identity for grouping in Store.Save.
// KeyedTable itself is not comparable with ==: postgres.Table is an interface
// over a column list (a slice inside). Schema plus table name is what
// distinguishes physical tables in SQL, and that is enough: two cells with
// different Parent/Key over one table would be a declaration error, not a
// legitimate configuration.
func (kt KeyedTable) groupKey() string {
	return kt.Src.SchemaName() + "." + kt.Src.TableName()
}

// read returns the "key -> string" map of one parent. Empty values (NULL or
// "") are left out: "no row" and "empty row" are indistinguishable on read,
// and the form document needs no second kind of emptiness.
func (kt KeyedTable) read(ctx context.Context, q Querier, col postgres.ColumnString, parentID string) (any, error) {
	sql, args := postgres.
		SELECT(kt.Key, col).
		FROM(kt.Src).
		WHERE(kt.parentEq(parentID)).
		// Fixed order for stability: the diff goes through fmt.Sprint (which
		// sorts map keys itself), but query logs and tests read easier when
		// rows arrive the same way.
		ORDER_BY(kt.Key.ASC()).
		Sql()

	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]any{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		if len(vals) != 2 {
			continue
		}
		key, _ := Normalize(vals[0]).(string)
		value, _ := Normalize(vals[1]).(string)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	return out, rows.Err()
}

// write applies the patch of one column: a thin wrapper over writeMany with a
// single entry in the group, used by the cell's own save when the grouping in
// Store.Save is bypassed.
//
// A key absent from the patch is not touched at all - "not sent means do not
// touch" holds inside the field too. Otherwise a form saved with one active
// key (one locale) would silently erase all the others.
func (kt KeyedTable) write(ctx context.Context, tx pgx.Tx, col postgres.ColumnString, parentID string, patch map[string]any, clear ClearPolicy) error {
	return kt.writeMany(ctx, tx, parentID, []keyedColumnWrite{{col: col, clear: clear, patch: patch}})
}

// keyedColumnWrite is the patch of one cell for the shared upsert of
// writeMany: its column, its clearing policy, its slice of the patch
// ({key: value}).
type keyedColumnWrite struct {
	col   postgres.ColumnString
	clear ClearPolicy
	patch map[string]any
}

// writeMany applies the patches of several columns of one side table, one
// row per key that occurs in at least one patch.
//
// Postgres checks NOT NULL on the tentative INSERT row before resolving ON
// CONFLICT, even when the conflicting row already exists and carries the
// value. So inserting one column at a time on a side table with several
// editable fields (say title NOT NULL next to a nullable description) breaks
// on every save of description: title is missing from VALUES and the
// constraint fires before ON CONFLICT DO UPDATE gets a chance. One upsert with
// every column written in this save passes the check: title is in VALUES with
// its current value.
//
// Clearing (an empty value) stays a separate UPDATE on its own column: UPDATE
// does not check NOT NULL on columns absent from its SET, so there is nothing
// to merge.
//
// Keys are sorted: the SQL is logged, and a flickering statement order hurts
// both reading and the statement cache.
func (kt KeyedTable) writeMany(ctx context.Context, tx pgx.Tx, parentID string, writes []keyedColumnWrite) error {
	keySet := map[string]bool{}
	for _, w := range writes {
		for k := range w.patch {
			keySet[k] = true
		}
	}
	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		var toClear []keyedColumnWrite
		var toSet []keyedColumnWrite
		for _, w := range writes {
			raw, present := w.patch[key]
			if !present {
				continue
			}
			value, _ := raw.(string)
			// null and "" are the same thing: clearing. Different outcomes for a
			// form sending "" from a textarea and a client sending null would be
			// a trap.
			if raw == nil || value == "" {
				toClear = append(toClear, w)
				continue
			}
			toSet = append(toSet, w)
		}
		for _, w := range toClear {
			sql, args := kt.clearSQL(w.col, parentID, key, w.clear)
			if _, err := tx.Exec(ctx, sql, args...); err != nil {
				return err
			}
		}
		if len(toSet) == 0 {
			continue
		}
		sql, args := kt.upsertManySQL(parentID, key, toSet)
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return err
		}
	}
	return nil
}

// upsertManySQL is the SQL and parameters of one upsert carrying the values
// of every passed column at once. A separate method from writeMany, like
// insertValueSQL from replaceValues (child.go): a test must see the finished
// SQL without executing it, and it must be the same SQL that reaches the
// database.
//
// writes are already the non-empty entries (writeMany separates clearing
// beforehand): an empty list never arrives, the caller returns early - the
// same discipline as UpdateSQL.
func (kt KeyedTable) upsertManySQL(parentID, key string, writes []keyedColumnWrite) (string, []any) {
	cols := make(postgres.ColumnList, 0, len(writes)+2)
	cols = append(cols, kt.Parent, kt.Key)
	vals := make([]any, 0, len(writes)+2)
	vals = append(vals, kt.parentValue(parentID), kt.keyValue(key))
	assigns := make([]postgres.ColumnAssigment, 0, len(writes))

	for _, w := range writes {
		value, _ := w.patch[key].(string)
		cols = append(cols, w.col)
		vals = append(vals, postgres.String(value))
		assigns = append(assigns, w.col.SET(postgres.String(value)))
	}

	// The (parent, key) row is unique, so the upsert is one statement rather
	// than SELECT-then-INSERT: two concurrent users get "last one wins"
	// instead of a uniqueness error.
	return kt.Src.
		INSERT(cols).
		VALUES(vals[0], vals[1:]...).
		ON_CONFLICT(kt.Key, kt.Parent).
		DO_UPDATE(postgres.SET(assigns...)).
		Sql()
}

// clearSQL is the clearing query of one key under the chosen policy.
//
// Nulling is an UPDATE, not an upsert: the row may not exist at all (the key
// was never filled), and inserting a row of empty values just to clear it is
// pointless - "no row" and "empty value" are indistinguishable on read. An
// UPDATE touching zero rows is a normal outcome.
func (kt KeyedTable) clearSQL(col postgres.ColumnString, parentID, key string, clear ClearPolicy) (string, []any) {
	where := kt.parentEq(parentID).AND(kt.keyEq(key))
	if clear.deleteRow {
		return kt.Src.DELETE().WHERE(where).Sql()
	}
	empty := clear.value
	if empty == nil {
		empty = postgres.StringExp(postgres.NULL)
	}
	return kt.Src.UPDATE().SET(col.SET(empty)).WHERE(where).Sql()
}

// parentEq compares with the parent via ::text, as in Table.where: the query
// tolerates any garbage in the id and returns "no rows" instead of failing on
// the cast.
func (kt KeyedTable) parentEq(parentID string) postgres.BoolExpression {
	return postgres.CAST(kt.Parent).AS_TEXT().EQ(postgres.String(parentID))
}

// keyEq compares the key in its own type (text does not work for an enum
// column).
func (kt KeyedTable) keyEq(key string) postgres.BoolExpression {
	return kt.Key.EQ(kt.keyValue(key))
}

// parentValue/keyValue build the parameter in the column's type: for an
// insert ::text does not work, the value goes into the column rather than
// being compared with it.
func (kt KeyedTable) parentValue(parentID string) postgres.StringExpression {
	return castString(parentID, kt.ParentType)
}

func (kt KeyedTable) keyValue(key string) postgres.StringExpression {
	return castString(key, kt.KeyType)
}

func castString(value, sqlType string) postgres.StringExpression {
	if sqlType == "" {
		return postgres.String(value)
	}
	return postgres.StringExp(postgres.CAST(postgres.String(value)).AS(sqlType))
}
