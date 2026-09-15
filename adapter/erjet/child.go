package erjet

import (
	"context"
	"fmt"
	"sort"

	"github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5"

	"github.com/qrotux/editrig-go/decl"
	"github.com/qrotux/editrig-go/ui"
)

// ChildTable is a child table: a repeatable block of the parent, one row per
// element, with the position in its own column.
//
// It is a storage shape, not a schema convention, with the same role as
// KeyedTable and RelsTable: the type knows how rows relate to the parent and
// how they are ordered, and nothing about what an element is. The second
// constructor argument says that: one cell (ChildValues) or a nested
// declaration (ChildRows).
//
// It is a separate type rather than a RelsTable parameter because there an
// element is the id of a foreign row in a shared rels table distinguished by
// Path, positions start at zero; here an element is the table's own row with
// its own columns, there is no Path, and positions start at one (writeOrder).
type ChildTable struct {
	Src postgres.Table
	// ID is the element row's string or integer PK. It is the target of foreign
	// keys from nested
	// side tables, which is why the object projection preserves it instead of
	// regenerating it.
	ID postgres.Column
	// Parent is the string or integer foreign key to the owning row.
	Parent postgres.Column
	Order  postgres.ColumnInteger
	// Key partitions rows within one parent (a locale held in the row itself);
	// the lists of different partitions are independent, with distinct ids and
	// lengths. nil means no partition.
	Key postgres.ColumnString
	// ParentType/KeyType/IDType are SQL type names for an explicit parameter
	// cast, as in KeyedTable: the parent is usually uuid, the key an enum, and
	// the parameter travels as text. Without the cast Postgres fails, and only
	// on a live database.
	//
	// IDType matters where the id is inserted, not only compared: comparison
	// goes through ::text (scope), insertion goes into the column, and a uuid
	// PK rejects an uncast text parameter. Empty for varchar PKs.
	ParentType string
	KeyType    string
	IDType     string
	// NewID generates the id of a new row. A hook, not a built-in generator:
	// the id format is a schema convention, and this package declares only the
	// storage shape. nil means the id column takes no part in INSERT at all
	// (a uuid PK with DEFAULT gen_random_uuid() needs no id from outside).
	NewID func() string
}

// --- shared by reads and writes ----------------------------------------------

// requireColumns panics at declaration time when ID, Parent or Order is nil,
// the three columns without which none of the four constructors can build a
// query. An unset struct field compiles silently and would otherwise fail as
// a nil interface inside go-jet on the first Load of a live form (selectSQL
// orders by ID and Order, scope filters by Parent). Each is named so the
// message points at the missing declaration field, not at the crash site.
func (ct ChildTable) requireColumns(ctor string) {
	if ct.ID == nil {
		panic("erjet: " + ctor + " requires ChildTable.ID — every read orders by it (ct.ID.ASC()) and the object projection needs it as the ON CONFLICT target, so a nil column would panic on the first Load of a live form")
	}
	if ct.Parent == nil {
		panic("erjet: " + ctor + " requires ChildTable.Parent — scope() filters every read and every delete by it, so a nil column would panic on the first Load of a live form")
	}
	if ct.Order == nil {
		panic("erjet: " + ctor + " requires ChildTable.Order — reads order by it and writes assign it, so a nil column would panic on the first Load of a live form")
	}
}

// scope is "rows of this parent", plus the partition when one is declared.
//
// The parent is compared through ::text, as in Table.where, so garbage in the
// id yields "no rows" instead of a cast failure. The key is compared in its
// own type: text does not match an enum column.
func (ct ChildTable) scope(parentID, key string) postgres.BoolExpression {
	where := postgres.CAST(ct.Parent).AS_TEXT().EQ(postgres.String(parentID))
	if ct.Key != nil && key != "" {
		where = where.AND(ct.Key.EQ(castString(key, ct.KeyType)))
	}
	return where
}

// selectSQL selects the projections of one parent's elements in position
// order.
//
// postgres.SELECT requires at least one projection, so an empty element
// declaration is a caller error and panics, as Table.RowSQL does, rather than
// building a nonsense query.
//
// The secondary sort key is mandatory: _order can be NULL in legacy data, and
// without it the order of such rows would change from query to query (same
// reason as in RelsTable.read).
func (ct ChildTable) selectSQL(projs []postgres.Projection, parentID, key string) (string, []any) {
	if len(projs) == 0 {
		panic("erjet: ChildTable.selectSQL with no projections — caller must not pass an empty element declaration")
	}
	return postgres.SELECT(projs[0], projs[1:]...).
		FROM(ct.Src).
		WHERE(ct.scope(parentID, key)).
		ORDER_BY(ct.Order.ASC(), ct.ID.ASC()).
		Sql()
}

// rows returns the raw values of the element rows. An empty result is an empty
// slice, not nil: the form field is a list, and null would reach the widget.
//
// The slice from rows.Values() is kept without a copy, as in RelsTable.read:
// pgx v5 allocates a fresh values slice on every Values() call, so there is no
// reused buffer to defend against.
func (ct ChildTable) rows(ctx context.Context, q Querier, projs []postgres.Projection, parentID, key string) ([][]any, error) {
	sql, args := ct.selectSQL(projs, parentID, key)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := [][]any{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		out = append(out, vals)
	}
	return out, rows.Err()
}

// writeOrder is the position column value for the i-th payload element.
//
// 1-based, unlike RelsTable (0-based): that is how existing data is stored. A
// zero would put the first element of our save before every existing row, and
// only the eye would notice, on a live site.
func writeOrder(i int) postgres.IntegerExpression { return postgres.Int(int64(i + 1)) }

// --- writes: a list of SCALARS (ChildValues/KeyedChildValues) -----------------

// ChildValues is a satellite cell holding an ordered list of scalars in a
// child table.
//
// On the wire the field is an array of one column's values (["uuid", ...]); the
// row id does not travel, a list of objects wrapped around an internal id would
// read noticeably worse.
//
// The write is a replacement (DELETE of the partition + INSERT by position), as
// in Rels. Hence a mandatory precondition, the same as ClearDeleteRow's: the
// cell is suitable only if nothing references this child table's ids. If
// something did, ON DELETE CASCADE would silently take the nested rows away on
// every save. ChildRows exists for such a table, it preserves ids.
//
// Second precondition: ct.Key must be nil; the gate below panics otherwise.
// Without it a partitioned table would break silently in both directions:
// readValues calls scope(parentID, "") with no key filter, so every partition
// arrives on the form as one flat list, and a save with an empty list (the
// user cleared the field) calls deleteAllSQL(parentID, "") with the same
// unfiltered scope and wipes the rows of every partition in one DELETE without
// inserting anything back, committed, without a single error. A partitioned
// table needs KeyedChildValues.
//
// The argument cell's value is reused as is (UUID, Str and the like already
// know how to write their element), so no second element parser exists here.
// Hence the gates: cell.value must be set, because a cell that cannot produce
// an expression can never insert a row (ReadOnly/JSON have no write path at
// all, and Deferred materializes its value in a second phase, after the row
// exists); and cell.proj must be a column, not an arbitrary projection,
// because INSERT ... VALUES needs a column on the left. Panicking here rather
// than failing silently in Save surfaces the defect at declaration time.
func ChildValues(ct ChildTable, cell Column) Column {
	ct.requireColumns("ChildValues")
	if cell.value == nil {
		panic("erjet: ChildValues requires an element cell that can produce a value expression for INSERT ... VALUES — this cell provides none, so it could never insert a row (cells without value: erjet.ReadOnly/JSON have no write path at all; erjet.Deferred materializes its value in a second phase, after the row exists, and can never produce one)")
	}
	if _, ok := cell.proj.(postgres.Column); !ok {
		panic("erjet: ChildValues requires an element cell whose projection is a column — INSERT ... VALUES has no target otherwise")
	}
	if ct.Key != nil {
		panic("erjet: ChildValues cannot be used on a partitioned ChildTable (Key is set) — use KeyedChildValues, or reads silently merge every locale into one list and an empty save silently wipes every locale for the parent")
	}
	return Column{
		wire: ui.Wire{Kind: ui.KindList, Elem: cell.wire.Kind},
		load: func(ctx context.Context, q Querier, parentID string) (any, error) {
			return ct.readValues(ctx, q, cell, parentID, "")
		},
		save: func(ctx context.Context, tx pgx.Tx, parentID string, v any) (any, error) {
			list, ok := v.([]any)
			if !ok {
				return nil, nil
			}
			if err := ct.replaceValues(ctx, tx, cell, parentID, "", list); err != nil {
				return nil, err
			}
			// Re-read after the write, in the same transaction, as KeyedStrings
			// does: the audit diff compares the full "before" with the full
			// "after", and returning the input would report something other
			// than what landed in the table.
			return ct.readValues(ctx, tx, cell, parentID, "")
		},
	}
}

// KeyedChildValues is ChildValues with one list per partition key.
//
// On the wire it is a map {"en": [...], "ru": [...]}, and "absent key means
// untouched" applies per partition: a form saved with one active locale does
// not erase the others. Same rule and reason as inside KeyedStrings' map, one
// level deeper.
//
// Precondition, the mirror of ChildValues: ct.Key must be set. Without the
// gate an unpartitioned table would fail on the first read, since
// readKeyedValues puts ct.Key (a nil interface) into postgres.SELECT as the
// first projection.
func KeyedChildValues(ct ChildTable, cell Column) Column {
	ct.requireColumns("KeyedChildValues")
	if cell.value == nil {
		panic("erjet: KeyedChildValues requires an element cell that can produce a value expression for INSERT ... VALUES — this cell provides none, so it could never insert a row (cells without value: erjet.ReadOnly/JSON have no write path at all; erjet.Deferred materializes its value in a second phase, after the row exists, and can never produce one)")
	}
	if _, ok := cell.proj.(postgres.Column); !ok {
		panic("erjet: KeyedChildValues requires an element cell whose projection is a column — INSERT ... VALUES has no target otherwise")
	}
	if ct.Key == nil {
		panic("erjet: KeyedChildValues requires a partitioned ChildTable (Key must be set) — use ChildValues for a table without partitions")
	}
	return Column{
		wire: ui.Wire{Kind: ui.KindMap, Elem: ui.KindList},
		load: func(ctx context.Context, q Querier, parentID string) (any, error) {
			return ct.readKeyedValues(ctx, q, cell, parentID)
		},
		save: func(ctx context.Context, tx pgx.Tx, parentID string, v any) (any, error) {
			patch, ok := v.(map[string]any)
			if !ok {
				return nil, nil
			}
			for _, key := range sortedMapKeys(patch) {
				if key == "" {
					// An empty key is no partition: scope() treats it as absent,
					// so replaceValues with key="" would DELETE every partition
					// at once. Skip the garbage key instead of merging.
					continue
				}
				list, ok := patch[key].([]any)
				if !ok {
					// Not a list: leave the partition alone. Erasing it would be
					// the worst outcome, the user sent garbage, not an empty list.
					continue
				}
				if err := ct.replaceValues(ctx, tx, cell, parentID, key, list); err != nil {
					return nil, err
				}
			}
			return ct.readKeyedValues(ctx, tx, cell, parentID)
		},
	}
}

// readValues returns one partition's values in position order.
func (ct ChildTable) readValues(ctx context.Context, q Querier, cell Column, parentID, key string) (any, error) {
	rows, err := ct.rows(ctx, q, []postgres.Projection{cell.proj}, parentID, key)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, row := range rows {
		if len(row) != 1 {
			continue
		}
		out = append(out, decodeWith(cell, row[0]))
	}
	return out, nil
}

// readKeyedValues returns the "key -> list" map. Keys with no rows are absent:
// which ones to show empty is the schema's decision (it knows the active keys,
// the store does not), the same boundary as KeyedTable.read.
//
// The query deliberately runs with key="": it reads every partition of the
// parent at once, and ct.Key is part of the projection, so each row says which
// partition it belongs to. The split happens here, not in scope.
func (ct ChildTable) readKeyedValues(ctx context.Context, q Querier, cell Column, parentID string) (any, error) {
	rows, err := ct.rows(ctx, q, []postgres.Projection{ct.Key, cell.proj}, parentID, "")
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, row := range rows {
		if len(row) != 2 {
			continue
		}
		key, _ := Normalize(row[0]).(string)
		if key == "" {
			continue
		}
		list, _ := out[key].([]any)
		out[key] = append(list, decodeWith(cell, row[1]))
	}
	return out, nil
}

// replaceValues rewrites the whole partition.
//
// One INSERT statement per element: lists here are tens of rows, and one
// statement per element reads well in the log (same decision as
// RelsTable.replace).
//
// key must be a real partition key or literally "" for an unpartitioned table;
// callers guarantee it (ChildValues always sends "", KeyedChildValues drops an
// empty patch key before calling). It is not re-checked here: DELETE and
// INSERT below use the same key and fail together.
func (ct ChildTable) replaceValues(ctx context.Context, tx pgx.Tx, cell Column, parentID, key string, list []any) error {
	del, args := ct.deleteAllSQL(parentID, key)
	if _, err := tx.Exec(ctx, del, args...); err != nil {
		return err
	}
	// pos is a separate position counter, not the loop index: an unparsed
	// element skips the insert but must not consume a number, or positions
	// would come out as 1,3,4 and silently stop being consecutive.
	pos := 0
	for _, v := range list {
		sql, args, ok := ct.insertValueSQL(cell, parentID, key, v, pos)
		if !ok {
			// Skip the element, not the whole list: unlike an array column, the
			// DELETE has already run, and an early return would leave the
			// partition half rewritten.
			continue
		}
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return err
		}
		pos++
	}
	return nil
}

// deleteAllSQL is the DELETE half of the replacement: the whole partition, or
// every row of the parent when there is no key.
func (ct ChildTable) deleteAllSQL(parentID, key string) (string, []any) {
	return ct.Src.DELETE().WHERE(ct.scope(parentID, key)).Sql()
}

// insertValueSQL builds the INSERT of one list element. A separate method from
// replaceValues so a test can see the finished SQL without executing a write
// (ExportChildInsertValueSQL), built by the same code that replaceValues runs.
//
// The INSERT columns are four explicit branches rather than a dynamic list:
// ChildTable has exactly two independent flags (NewID present, real key
// present), so four shapes are cheaper to enumerate than to abstract.
//
// A dynamic list is possible, and upsertRowSQL needs one, so the go-jet gotcha
// is recorded here: spreading a []postgres.Column into Table.INSERT(...) does
// not compile, since INSERT takes go-jet's internal jet.Column, which cannot
// be named outside the module (checked on v2.15.0). Passing one
// postgres.ColumnList value does compile: ColumnList implements Column, so it
// passes as a single argument, and INSERT unwraps it back into separate
// columns before serializing names (verified: the generated SQL names every
// column, not one empty one).
func (ct ChildTable) insertValueSQL(cell Column, parentID, key string, v any, i int) (string, []any, bool) {
	expr, _, ok := cell.value(v)
	if !ok {
		return "", nil, false
	}
	valCol := cell.proj.(postgres.Column)
	parent := castString(parentID, ct.ParentType)
	order := writeOrder(i)
	withKey := ct.Key != nil && key != ""

	var stmt postgres.InsertStatement
	switch {
	case ct.NewID != nil && withKey:
		stmt = ct.Src.INSERT(ct.ID, ct.Parent, ct.Order, ct.Key, valCol).
			VALUES(castString(ct.NewID(), ct.IDType), parent, order, castString(key, ct.KeyType), expr)
	case ct.NewID != nil:
		stmt = ct.Src.INSERT(ct.ID, ct.Parent, ct.Order, valCol).
			VALUES(castString(ct.NewID(), ct.IDType), parent, order, expr)
	case withKey:
		stmt = ct.Src.INSERT(ct.Parent, ct.Order, ct.Key, valCol).
			VALUES(parent, order, castString(key, ct.KeyType), expr)
	default:
		stmt = ct.Src.INSERT(ct.Parent, ct.Order, valCol).
			VALUES(parent, order, expr)
	}
	sql, args := stmt.Sql()
	return sql, args, true
}

// --- writes: a list of OBJECTS (ChildRows/KeyedChildRows) ---------------------

// ChildRows is a satellite cell holding an ordered list of objects in a child
// table.
//
// The element is described by a nested decl.Set, the same kind as the entity's,
// so there is no second list of element fields: one set feeds both this cell
// and the ui.Items composition (decl.Entry.Items).
//
// The write is an upsert by id, not a replacement, and not as an optimization:
// a child table's id can be the target of a foreign key from its own side
// table (a locales table with ON DELETE CASCADE). A replacement would silently
// take every element's translations away on each save, while a "list survived"
// test stayed green: elements present, values right, order right.
//
// The element id comes from the form and is not trusted: validate.StripReadonly cleans
// only the top level of the payload, so a read-only id inside an element does
// reach Save; the conflict target ON CONFLICT (id) is the global PK while the
// DELETE is bounded by the parent, so a forged id of a foreign row would update
// that row. Ids are therefore checked against the ones this parent actually
// owns (ownedIDs, in the same transaction as the write), and an unknown one
// degrades to "a new element was added" (resolveIDs).
//
// Precondition, the mirror of ChildValues: ct.Key must be nil. The gate panics
// at declaration time because a partitioned table would break silently in both
// directions: readRows calls scope(parentID, "") with no key filter, so every
// partition arrives as one list, and deleteExceptSQL with the same unfiltered
// scope wipes the rows of every other partition, whose ids are by construction
// not in this partition's keep-list. KeyedChildRows exists for such a table.
func ChildRows(ct ChildTable, d decl.Set[Column]) Column {
	ct.requireColumns("ChildRows")
	if ct.Key != nil {
		panic("erjet: ChildRows cannot be used on a partitioned ChildTable (Key is set) — use KeyedChildRows, or reads merge every partition into one list and a save deletes every row of the other partitions")
	}
	inner, idField := childElement(ct, d, "ChildRows")
	return Column{
		wire: ui.Wire{Kind: ui.KindItems},
		load: func(ctx context.Context, q Querier, parentID string) (any, error) {
			return ct.readRows(ctx, q, inner, idField, parentID, "")
		},
		save: func(ctx context.Context, tx pgx.Tx, parentID string, v any) (any, error) {
			list, ok := v.([]any)
			if !ok {
				// Not a list: nothing parsed, nothing to write. Same decision as
				// ok==false in assign; without it a string payload would wipe
				// the whole list with one DELETE.
				return nil, nil
			}
			if err := ct.writeRows(ctx, tx, inner, idField, parentID, "", list); err != nil {
				return nil, err
			}
			// Re-read after the write, in the same transaction, as KeyedStrings
			// and ChildValues do: the audit diff compares the full "before"
			// with the full "after", and the input does not even know the ids
			// of new elements.
			return ct.readRows(ctx, tx, inner, idField, parentID, "")
		},
	}
}

// KeyedChildRows is ChildRows with one list per partition key.
//
// On the wire it is a map {"en": [...], "ru": [...]}, and "absent key means
// untouched" applies per partition, as in KeyedChildValues: a form saved with
// one active locale does not erase the others.
//
// Precondition, the mirror of ChildRows: ct.Key must be set, otherwise
// readKeyedRows puts ct.Key (a nil interface) into postgres.SELECT as the
// first projection.
//
// The ui half is ui.Keyed(ui.Items(), keys): Keyed puts the $keyedItems marker
// on the outer field (ui/field.go), decl.Set.Fields adds the element
// composition through WithKeyedItems + KeyLabels, the type axis sees
// {KindMap, KindItems}, and both lints take their own branch. Expected call
// site:
//
//	{Name: "blocks", Col: erjet.KeyedChildRows(child, blockDecl),
//	    Items: blockDecl, UI: ui.Keyed(ui.Items(), ui.Keys("en", "ru"))}
//
// The live run is storetest.RunKeyedChildRows.
func KeyedChildRows(ct ChildTable, d decl.Set[Column]) Column {
	ct.requireColumns("KeyedChildRows")
	if ct.Key == nil {
		panic("erjet: KeyedChildRows requires a partitioned ChildTable (Key must be set) — use ChildRows for a table without partitions")
	}
	inner, idField := childElement(ct, d, "KeyedChildRows")
	return Column{
		wire: ui.Wire{Kind: ui.KindMap, Elem: ui.KindItems},
		load: func(ctx context.Context, q Querier, parentID string) (any, error) {
			return ct.readKeyedRows(ctx, q, inner, idField, parentID)
		},
		save: func(ctx context.Context, tx pgx.Tx, parentID string, v any) (any, error) {
			patch, ok := v.(map[string]any)
			if !ok {
				return nil, nil
			}
			for _, key := range sortedMapKeys(patch) {
				if key == "" {
					// An empty key is no partition: scope() treats it as absent,
					// so writeRows with key="" would delete the "extra" rows of
					// every partition at once.
					continue
				}
				list, ok := patch[key].([]any)
				if !ok {
					// Not a list: leave the partition alone, the user sent
					// garbage, not an empty list.
					continue
				}
				if err := ct.writeRows(ctx, tx, inner, idField, parentID, key, list); err != nil {
					return nil, err
				}
			}
			return ct.readKeyedRows(ctx, tx, inner, idField, parentID)
		},
	}
}

// childElement runs the gates shared by both object projections at declaration
// time rather than on the first Save of a live form, and returns the element's
// persistent projection plus the name of the field carrying its id. ctor names
// the constructor for the messages.
func childElement(ct ChildTable, d decl.Set[Column], ctor string) (Table, string) {
	// ct.ID is already non-nil: both callers start with ct.requireColumns. There
	// is deliberately no second copy of that check here, two panics for one
	// condition drift apart on the first edit and the dead one starts lying. A
	// third caller must start with requireColumns too.
	inner := NewTable(d, ct.Src, ct.ID)

	idField, ok := inner.idField()
	if !ok {
		panic("erjet: " + ctor + " requires the element declaration to map one field onto ChildTable.ID — without it the id never reaches the form, every element comes back looking new, and each save deletes the old rows (taking their nested rows with them through ON DELETE CASCADE)")
	}
	idCell, _ := inner.Column(idField)
	if idCell.Writable() {
		panic("erjet: " + ctor + " requires the element's id field to be read-only (erjet.ReadOnly) — the id is taken from the database or from NewID, never from the payload, so a writable declaration would render an input that silently does nothing")
	}
	// Child ids use the identifier decoder in both rowItem and ownedIDs. A
	// custom cell decoder would disagree with ownership checks.
	if idCell.decode != nil {
		panic("erjet: " + ctor + " requires the element's id field to use erjet.ReadOnly without a custom decoder — a custom decoder makes the submitted id disagree with ownership checks")
	}

	satellites := false
	for _, nc := range inner.Cols {
		if nc.Name == idField {
			continue
		}
		if nc.Column.IsSatellite() {
			satellites = true
			continue
		}
		if !nc.Column.Writable() {
			continue // ReadOnly/JSON: read with the rest, never written
		}
		// A writable element column needs both halves of the write: value for
		// the INSERT ... VALUES of a new element and assign for the DO UPDATE of
		// an existing one. The message names the condition, not a list of
		// constructors; the only stock cell that trips it is erjet.Deferred,
		// whose value materializes in a second phase, after the row exists.
		if nc.Column.value == nil || nc.Column.assign == nil {
			panic("erjet: " + ctor + " requires every writable element cell to provide both halves of the write — value for the INSERT ... VALUES of a new element and assign for the DO UPDATE of an existing one; " + nc.Name + " provides only assign (erjet.Deferred is such a cell by construction — its value materializes only in the second phase, after the row exists), so it would survive an update and vanish on insert")
		}
		if _, ok := nc.Column.proj.(postgres.Column); !ok {
			panic("erjet: " + ctor + " requires every writable element cell's projection to be a column — INSERT ... VALUES has no target otherwise (field " + nc.Name + ")")
		}
	}
	// A nested satellite is parented on the element's id, and for a new element
	// that id comes only from NewID: without the generator the database would
	// assign it, it would be unknown here, and the new element's nested rows
	// would become orphans with an empty parent, while existing elements kept
	// working.
	if satellites && ct.NewID == nil {
		panic("erjet: " + ctor + " requires ChildTable.NewID when the element declares satellite cells — a new element's id would be generated by the database and unknown here, so its nested rows would be parented on an empty id")
	}
	return inner, idField
}

// readRows returns one parent's elements: column fields in one SELECT, then
// each element's satellites with their own queries.
//
// N+1 per element is accepted deliberately, the same trade as Table.Row:
// elements number in the units, and the alternative would make a cell build a
// fragment of someone else's query instead of a query.
func (ct ChildTable) readRows(ctx context.Context, q Querier, inner Table, idField, parentID, key string) (any, error) {
	cols, sats := inner.split(inner.Order())
	raw, err := ct.rows(ctx, q, inner.projsOf(cols), parentID, key)
	if err != nil {
		return nil, err
	}
	// An empty array, not nil: the form field is a list, and null would reach
	// the widget.
	out := []any{}
	for _, row := range raw {
		item, err := ct.rowItem(ctx, q, inner, idField, cols, sats, row)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

// readKeyedRows returns the "key -> list of objects" map. Keys with no rows are
// absent, the schema decides which to show empty, as in readKeyedValues; and
// the same technique: the query deliberately runs with key="" (every partition
// of the parent at once) with ct.Key as the first projection, so each row says
// which partition it belongs to.
func (ct ChildTable) readKeyedRows(ctx context.Context, q Querier, inner Table, idField, parentID string) (any, error) {
	cols, sats := inner.split(inner.Order())
	projs := append([]postgres.Projection{ct.Key}, inner.projsOf(cols)...)
	raw, err := ct.rows(ctx, q, projs, parentID, "")
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, row := range raw {
		if len(row) != len(cols)+1 {
			// An error, not a skipped row, exactly as in rowItem: misaligned
			// values would land in the wrong fields, and a silently swallowed
			// element would look absent, so the next save would delete its row.
			return nil, fmt.Errorf("erjet: %d values for %d element columns plus the partition key", len(row), len(cols))
		}
		key, _ := Normalize(row[0]).(string)
		if key == "" {
			continue
		}
		item, err := ct.rowItem(ctx, q, inner, idField, cols, sats, row[1:])
		if err != nil {
			return nil, err
		}
		list, _ := out[key].([]any)
		out[key] = append(list, item)
	}
	return out, nil
}

// rowItem turns one child row into a form object: column values through their
// own decodes, then the satellites read by this element's id.
func (ct ChildTable) rowItem(ctx context.Context, q Querier, inner Table, idField string, cols, sats []string, row []any) (map[string]any, error) {
	if len(row) != len(cols) {
		// Not a formality: misaligned values would silently land in the wrong
		// fields. Same check and reason as in Table.rowCols.
		return nil, fmt.Errorf("erjet: %d values for %d element columns", len(row), len(cols))
	}
	item := make(map[string]any, len(cols)+len(sats))
	for i, name := range cols {
		c, _ := inner.Column(name)
		if col, ok := c.proj.(postgres.Column); ok && sameColumn(col, inner.ID) {
			item[name] = normalizeIdentifier(row[i])
		} else {
			item[name] = decodeWith(c, row[i])
		}
	}
	id, _ := item[idField].(string)
	for _, name := range sats {
		c, _ := inner.Column(name)
		v, err := c.load(ctx, q, id)
		if err != nil {
			return nil, err
		}
		item[name] = v
	}
	return item, nil
}

// writeRows deletes the extra rows, then upserts each element, then runs the
// second phase of its satellites, all in the given transaction.
//
// The order of the three steps is load-bearing. ownedIDs runs before the
// DELETE: afterwards a deleted row is indistinguishable from one that never
// existed, and "own id" becomes undecidable. The DELETE runs before the
// upserts: on a table without NewID the database assigns a new row's id, which
// is by construction not in the keep-list, so a DELETE after the insert would
// remove exactly what was just inserted. Satellites run after their element's
// upsert: before it the parent row may not exist yet, and inserting a
// translation would violate the foreign key.
func (ct ChildTable) writeRows(ctx context.Context, tx pgx.Tx, inner Table, idField, parentID, key string, list []any) error {
	owned, err := ct.ownedIDs(ctx, tx, parentID, key)
	if err != nil {
		return err
	}
	ids := ct.resolveIDs(idField, owned, list)

	del, args := ct.deleteExceptSQL(parentID, key, ids)
	if _, err := tx.Exec(ctx, del, args...); err != nil {
		return err
	}

	// pos is a separate position counter, not the loop index: an unparsed
	// element skips the insert but must not consume a number, or positions
	// would silently stop being consecutive (same reason as pos in
	// replaceValues).
	pos := 0
	for i, raw := range list {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if err := ct.upsertRow(ctx, tx, inner, idField, parentID, key, ids[i], pos, item); err != nil {
			return err
		}
		// Nested satellites are parented on the element's id, which is exactly
		// why ChildRows cannot write by replacement. Their wire echo is not
		// needed: the cell's save re-reads the whole list afterwards, and the
		// audit diff gets what was read, not what was assembled piecemeal.
		for _, nc := range inner.Deferred(item) {
			if _, err := nc.Column.save(ctx, tx, ids[i], item[nc.Name]); err != nil {
				return err
			}
		}
		pos++
	}
	return nil
}

// ownedIDs returns the ids of the rows that actually belong to this parent (and
// partition).
//
// Read with the same scope as everything else: if it diverged from the DELETE,
// "own" would stop meaning "mine" and the forgery gate would be decoration.
// Read in the same tx as the write: no statement of ours runs between the
// ownership snapshot and the upserts. A foreign statement re-parenting a row
// in between is theoretically possible under READ COMMITTED, but this code
// never changes a child row's parent.
func (ct ChildTable) ownedIDs(ctx context.Context, q Querier, parentID, key string) (map[string]bool, error) {
	raw, err := ct.rows(ctx, q, []postgres.Projection{ct.ID}, parentID, key)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(raw))
	for _, row := range raw {
		if len(row) != 1 {
			continue
		}
		if id := normalizeIdentifier(row[0]); id != "" {
			out[id] = true
		}
	}
	return out, nil
}

// resolveIDs returns the ids under which the payload elements land in the
// table.
//
// A foreign or unknown id is replaced with a fresh one (see ChildRows). An own
// row is recognized by ownership, not by existence: "such a row exists" says
// nothing about whose it is.
//
// An own id submitted twice also degrades to a new element: two upserts on one
// PK would collapse two form elements into one row, the second silently eating
// the first, while the keep-list looked legitimate.
func (ct ChildTable) resolveIDs(idField string, owned map[string]bool, list []any) []string {
	out := make([]string, len(list))
	used := make(map[string]bool, len(list))
	for i, raw := range list {
		item, ok := raw.(map[string]any)
		if !ok {
			// Not an object, so not an element: writeRows skips it. No id is
			// issued either, or a fresh NewID() would travel into the keep-list
			// as a DELETE parameter, protecting a row that does not exist. The
			// definition of "element" must be the same in both functions.
			continue
		}
		id, _ := item[idField].(string)
		if id != "" && owned[id] && !used[id] {
			used[id] = true
			out[i] = id
			continue
		}
		if ct.NewID != nil {
			out[i] = ct.NewID()
		}
		// No NewID: the id stays empty and the insert omits the id column, the
		// database generates it (DEFAULT gen_random_uuid()). Such a declaration
		// cannot have satellites; childElement forbids it.
	}
	return out
}

// deleteExceptSQL deletes the partition's elements that are not in the payload.
//
// An empty keep-list is legitimate input (every element was removed) and the
// branch is mandatory: NOT IN with an empty set is a syntax error, a 500 on
// every such form. Comparison goes through ::text, as in scope: the id column
// may be uuid while the parameter travels as text.
func (ct ChildTable) deleteExceptSQL(parentID, key string, keep []string) (string, []any) {
	where := ct.scope(parentID, key)
	vals := make([]postgres.Expression, 0, len(keep))
	for _, id := range keep {
		if id == "" {
			continue // an element without an id does not exist yet, nothing to exclude
		}
		vals = append(vals, postgres.String(id))
	}
	if len(vals) > 0 {
		where = where.AND(postgres.CAST(ct.ID).AS_TEXT().NOT_IN(vals...))
	}
	return ct.Src.DELETE().WHERE(where).Sql()
}

// upsertRow writes one element while preserving the row's identity.
func (ct ChildTable) upsertRow(ctx context.Context, tx pgx.Tx, inner Table, idField, parentID, key, id string, pos int, item map[string]any) error {
	sql, args := ct.upsertRowSQL(inner, idField, parentID, key, id, pos, item)
	_, err := tx.Exec(ctx, sql, args...)
	return err
}

// upsertRowSQL builds the upsert of one element. A separate method from
// upsertRow, like insertValueSQL from replaceValues: a test must see the
// finished SQL without executing a write (ExportChildUpsertRowSQL), and it must
// be the same SQL that reaches the database.
//
// The column list is dynamic here, unlike the four branches of insertValueSQL,
// because its composition comes from the element declaration. It is passed as
// one postgres.ColumnList value, the go-jet gotcha recorded at insertValueSQL.
//
// Columns absent from the payload are left out of the insert entirely: "not
// sent means untouched" holds inside an element too. Written unconditionally,
// an absent key would travel as NULL and zero the column on every save of a
// neighbouring field.
//
// INSERT and DO UPDATE are assembled in one pass: if the halves diverged, a
// column would be in one and not the other, and the field would save only for
// new elements (or only for existing ones), silently.
//
// id=="" happens only on a table without NewID: there is nothing to conflict
// on (the id column is not inserted at all), and ON CONFLICT would be a lie,
// comparing NULL and inserting a duplicate anyway.
func (ct ChildTable) upsertRowSQL(inner Table, idField, parentID, key, id string, pos int, item map[string]any) (string, []any) {
	cols := make(postgres.ColumnList, 0, len(inner.Cols)+3)
	vals := make([]any, 0, len(inner.Cols)+3)
	// The position is always assigned: without it the DO UPDATE of an element
	// with no changed fields would have an empty SET (invalid SQL), and
	// reordering would never be saved, the list round-trips with the old order.
	assigns := []postgres.ColumnAssigment{ct.Order.SET(writeOrder(pos))}

	if id != "" {
		cols = append(cols, ct.ID)
		vals = append(vals, castString(id, ct.IDType))
	}
	cols = append(cols, ct.Parent, ct.Order)
	vals = append(vals, castString(parentID, ct.ParentType), writeOrder(pos))
	if ct.Key != nil && key != "" {
		cols = append(cols, ct.Key)
		vals = append(vals, castString(key, ct.KeyType))
	}

	for _, nc := range inner.Cols {
		// The declared id is never written: it comes from ownedIDs/NewID and is
		// already among the columns above, a second id column would break the
		// insert. Parent and position are row bookkeeping the declaration does
		// not declare.
		//
		// There is deliberately no deferred-cell filter here (Table.Assignments
		// has `nc.Column.save != nil && in[nc.Name] != nil`): such a cell cannot
		// occur in an element, childElement panics on it at declaration time
		// because its value is nil. Loosen that gate and this line needs the
		// same condition, or Deferred lands in the INSERT with a nil value.
		if nc.Name == idField || nc.Column.IsSatellite() || !nc.Column.Writable() {
			continue
		}
		v, present := item[nc.Name]
		if !present {
			continue
		}
		expr, _, okValue := nc.Column.value(v)
		assign, _, okAssign := nc.Column.assign(v)
		if !okValue || !okAssign {
			// Unparsed value: the column is written by neither half.
			continue
		}
		cols = append(cols, nc.Column.proj.(postgres.Column))
		vals = append(vals, expr)
		assigns = append(assigns, assign)
	}

	stmt := ct.Src.INSERT(cols).VALUES(vals[0], vals[1:]...)
	if id != "" {
		stmt = stmt.ON_CONFLICT(ct.ID).DO_UPDATE(postgres.SET(assigns...))
	}
	return stmt.Sql()
}

// decodeWith reads an element value through the cell's own decode, else
// Normalize. The same fork as in Table.rowCols, or wall-clock and jsonb values
// would arrive from a child table in a different shape than from the entity's
// own.
func decodeWith(cell Column, v any) any {
	if cell.decode != nil {
		return cell.decode(v)
	}
	return Normalize(v)
}

// sortedMapKeys gives a deterministic patch traversal: SQL goes to the log, and
// a flickering statement order hurts both reading and the statement cache (as
// in KeyedTable.write).
func sortedMapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
