package erjet

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/go-jet/jet/v2/postgres"

	"github.com/qrotux/editrig-go/decl"
	"github.com/qrotux/editrig-go/ui"
)

// Hatches over the unexported Column.value/assign. The nil guard matters: cells
// without a value (ReadOnly/JSON/Deferred, see
// TestNoValueOnDeliberatelyReadonlyCells) would panic on the nil func instead
// of returning a clean ok=false, and the test would lose the very case it is
// written for, that a missing value looks the same as a rejected one.
func ExportAssign(c Column, v any) (postgres.ColumnAssigment, any, bool) {
	if c.assign == nil {
		return nil, nil, false
	}
	return c.assign(v)
}

func ExportValue(c Column, v any) (postgres.Expression, any, bool) {
	if c.value == nil {
		return nil, nil, false
	}
	return c.value(v)
}

// ExportWire exposes the wire shape as a value rather than through Accepts:
// TestCellDeclaresWire compares by equality, not Match, because Match forgives
// KindAny on either side and a typo onto KindAny would pass it silently.
func ExportWire(c Column) ui.Wire { return c.wire }

// ExportChildSelectSQL exposes ChildTable.selectSQL so a test sees the final
// SQL string instead of peeking into the builder.
func ExportChildSelectSQL(ct ChildTable, projs []postgres.Projection, parentID, key string) (string, []any) {
	return ct.selectSQL(projs, parentID, key)
}

// ExportChildDeleteAllSQL exposes the DELETE half of a partition replacement
// (replaceValues) without executing it.
func ExportChildDeleteAllSQL(ct ChildTable, parentID, key string) (string, []any) {
	return ct.deleteAllSQL(parentID, key)
}

// ExportChildInsertValueSQL exposes the INSERT of one list element, built by
// the same code the write path (replaceValues) executes, so the check and the
// implementation cannot drift apart silently.
func ExportChildInsertValueSQL(ct ChildTable, cell Column, parentID, key string, v any, i int) (string, []any) {
	sql, args, _ := ct.insertValueSQL(cell, parentID, key, v, i)
	return sql, args
}

// ExportChildResolveIDs exposes the "this id is ours, that one is forged"
// decision without executing a write. idField is passed rather than hardcoded
// as "id" because the id field's name comes from the declaration (the cell
// projected onto ct.ID), see Table.idField.
func ExportChildResolveIDs(ct ChildTable, idField string, owned map[string]bool, list []any) []string {
	return ct.resolveIDs(idField, owned, list)
}

// ExportChildDeleteExceptSQL exposes the DELETE of surplus partition rows, the
// only statement of the object write that can erase data.
func ExportChildDeleteExceptSQL(ct ChildTable, parentID, key string, keep []string) (string, []any) {
	return ct.deleteExceptSQL(parentID, key, keep)
}

// ExportChildUpsertRowSQL exposes ChildTable.upsertRowSQL; the declaration goes
// through the same childElement as the constructors, so the test sees the SQL
// writeRows actually executes.
func ExportChildUpsertRowSQL(ct ChildTable, d decl.Set[Column], parentID, key, id string, pos int, item map[string]any) (string, []any) {
	inner, idField := childElement(ct, d, "ChildRows")
	return ct.upsertRowSQL(inner, idField, parentID, key, id, pos, item)
}

// ExportKeyedUpsertManySQL exposes KeyedTable.upsertManySQL, the shared upsert
// of several columns of one side table, built by the same code
// writeKeyedGroups executes.
//
// It filters by the presence of the key in each column's patch, the same
// discipline as writeMany: upsertManySQL itself expects already filtered
// writes, and the test must see the real behaviour of the stack rather than
// feed the builder input that violates its invariant.
func ExportKeyedUpsertManySQL(kt KeyedTable, parentID, key string, cols []postgres.ColumnString, clears []ClearPolicy, patches []map[string]any) (string, []any) {
	writes := make([]keyedColumnWrite, 0, len(cols))
	for i, c := range cols {
		if _, present := patches[i][key]; !present {
			continue
		}
		writes = append(writes, keyedColumnWrite{col: c, clear: clears[i], patch: patches[i]})
	}
	return kt.upsertManySQL(parentID, key, writes)
}

// ExportAssignedArg returns the value actually bound to the "<col> = $N"
// assignment inside the DO UPDATE half of a statement.
//
// It parses the finished SQL rather than exposing a method because
// upsertRowSQL binds _order twice, in VALUES and in SET, so "the arguments
// contain int64(2)" cannot tell which occurrence carries it: replace the SET
// assignment with a constant and VALUES still brings the two (verified by
// mutation). DO UPDATE is the only path of an existing row, i.e. every row
// when items are reordered, and a defect there shows only after the form is
// reloaded.
func ExportAssignedArg(sql string, args []any, col string) (any, bool) {
	at := strings.Index(sql, "DO UPDATE")
	if at < 0 {
		return nil, false
	}
	// A cast on the right ("day = $7::bigint") is not part of the parameter
	// number, so only the digits right after "$" are taken.
	m := regexp.MustCompile(regexp.QuoteMeta(col) + ` = \$(\d+)`).FindStringSubmatch(sql[at:])
	if m == nil {
		return nil, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 || n > len(args) {
		return nil, false
	}
	return args[n-1], true
}
