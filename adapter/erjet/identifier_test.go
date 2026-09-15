package erjet

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/go-jet/jet/v2/postgres"

	"github.com/qrotux/editrig-go/decl"
)

const preciseBigID = "9007199254740993"

func TestIntegerPrimaryKeyReadsAsAnExactString(t *testing.T) {
	id := postgres.IntegerColumn("id")
	name := postgres.StringColumn("name")
	src := postgres.NewTable("public", "integer_parents", "", id, name)
	d := decl.Set[Column]{
		{Name: "id", Col: ReadOnly(id)},
		{Name: "name", Col: Text(name)},
	}
	table := NewTable(d, src, id)
	q := &fixedRowsQuerier{rows: &multiRows{rows: [][]any{{int64(9007199254740993), "parent"}}}}

	got, err := table.Row(context.Background(), q, table.Order(), preciseBigID)
	if err != nil {
		t.Fatalf("Row: %v", err)
	}
	if got["id"] != preciseBigID {
		t.Errorf("id = %#v, want exact string %q", got["id"], preciseBigID)
	}
	if got["name"] != "parent" {
		t.Errorf("name = %#v, want parent", got["name"])
	}
	if _, ok := Normalize(int64(7)).(float64); !ok {
		t.Errorf("Normalize(int64) = %T, want the existing scalar number shape", Normalize(int64(7)))
	}
}

func TestIntegerChildIdentifiersReadAsExactStrings(t *testing.T) {
	id := postgres.IntegerColumn("id")
	parent := postgres.IntegerColumn("parent_id")
	order := postgres.IntegerColumn("position")
	name := postgres.StringColumn("name")
	src := postgres.NewTable("public", "integer_children", "", id, parent, order, name)
	ct := ChildTable{
		Src: src, ID: id, Parent: parent, Order: order,
		IDType: "bigint", ParentType: "bigint", NewID: func() string { return preciseBigID },
	}
	inner := NewTable(decl.Set[Column]{
		{Name: "id", Col: ReadOnly(id)},
		{Name: "name", Col: Text(name)},
	}, src, id)

	item, err := ct.rowItem(context.Background(), &recQuerier{}, inner, "id", []string{"id", "name"}, nil,
		[]any{int64(9007199254740993), "child"})
	if err != nil {
		t.Fatalf("rowItem: %v", err)
	}
	if item["id"] != preciseBigID {
		t.Errorf("child id = %#v, want exact string %q", item["id"], preciseBigID)
	}

	owned, err := ct.ownedIDs(context.Background(), &recQuerier{rows: [][]any{{int64(9007199254740993)}}}, "42", "")
	if err != nil {
		t.Fatalf("ownedIDs: %v", err)
	}
	if !owned[preciseBigID] {
		t.Errorf("ownedIDs = %v, want exact string key %q", owned, preciseBigID)
	}

	sql, args := ct.upsertRowSQL(inner, "id", "42", "", preciseBigID, 0, map[string]any{"name": "child"})
	if !strings.Contains(sql, "::bigint") {
		t.Errorf("integer child insert does not cast ids to bigint:\n%s", sql)
	}
	if !containsArg(args, preciseBigID) || !containsArg(args, "42") {
		t.Errorf("child insert args = %#v, want string ids %q and %q", args, preciseBigID, "42")
	}
}

func TestIntegerParentWorksWithKeyedAndRelationTables(t *testing.T) {
	parent := postgres.IntegerColumn("parent_id")
	key := postgres.StringColumn("locale")
	value := postgres.StringColumn("bio")
	side := postgres.NewTable("public", "integer_parent_locales", "", parent, key, value)
	kt := KeyedTable{Src: side, Parent: parent, Key: key, ParentType: "bigint"}
	sql, args := ExportKeyedUpsertManySQL(kt, preciseBigID, "en", []postgres.ColumnString{value},
		[]ClearPolicy{ClearSetNull()}, []map[string]any{{"en": "hello"}})
	if !strings.Contains(sql, "::bigint") || !containsArg(args, preciseBigID) {
		t.Errorf("keyed insert does not carry the exact bigint parent id: sql=%s args=%#v", sql, args)
	}

	relID := postgres.IntegerColumn("id")
	relOrder := postgres.IntegerColumn("position")
	relPath := postgres.StringColumn("path")
	target := postgres.IntegerColumn("target_id")
	relsSrc := postgres.NewTable("public", "integer_parent_rels", "", relID, parent, relPath, relOrder, target)
	rt := RelsTable{
		Src: relsSrc, ID: relID, Parent: parent, Path: relPath, Order: relOrder,
		ParentType: "bigint", TargetType: "bigint",
	}
	cell := Rels(rt, "related", target)
	got, err := cell.load(context.Background(), &recQuerier{rows: [][]any{{int64(9007199254740993)}}}, preciseBigID)
	if err != nil {
		t.Fatalf("Rels load: %v", err)
	}
	if fmt.Sprint(got) != "["+preciseBigID+"]" {
		t.Errorf("relation ids = %#v, want exact string id", got)
	}
	tx := &fakeTx{}
	if _, err := cell.save(context.Background(), tx, preciseBigID, []any{preciseBigID}); err != nil {
		t.Fatalf("Rels save: %v", err)
	}
	if len(tx.execArgs) != 2 || !containsArg(tx.execArgs[1], preciseBigID) {
		t.Errorf("relation insert args = %#v, want exact string ids", tx.execArgs)
	}
}

func containsArg(args []any, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
