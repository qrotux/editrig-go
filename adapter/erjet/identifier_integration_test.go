package erjet

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5"

	"github.com/qrotux/editrig-go"
	"github.com/qrotux/editrig-go/decl"
)

const integerIdentifierDDL = `
DROP TABLE IF EXISTS integer_identifier_rels, integer_identifier_children,
  integer_identifier_values, integer_identifier_locales, integer_identifier_targets,
  integer_identifier_parents CASCADE;
CREATE TABLE integer_identifier_parents (
  id bigint PRIMARY KEY,
  title text
);
CREATE TABLE integer_identifier_locales (
  parent_id bigint NOT NULL REFERENCES integer_identifier_parents(id) ON DELETE CASCADE,
  locale text NOT NULL,
  bio text,
  PRIMARY KEY (parent_id, locale)
);
CREATE TABLE integer_identifier_values (
  id bigserial PRIMARY KEY,
  parent_id bigint NOT NULL REFERENCES integer_identifier_parents(id) ON DELETE CASCADE,
  position integer NOT NULL,
  value text NOT NULL
);
CREATE TABLE integer_identifier_children (
  id bigint PRIMARY KEY,
  parent_id bigint NOT NULL REFERENCES integer_identifier_parents(id) ON DELETE CASCADE,
  position integer NOT NULL,
  title text
);
CREATE TABLE integer_identifier_targets (
  id bigint PRIMARY KEY
);
CREATE TABLE integer_identifier_rels (
  id bigserial PRIMARY KEY,
  parent_id bigint NOT NULL REFERENCES integer_identifier_parents(id) ON DELETE CASCADE,
  path text NOT NULL,
  position integer NOT NULL,
  target_id bigint REFERENCES integer_identifier_targets(id)
);`

func TestIntegerIdentifierLiveRoundTrip(t *testing.T) {
	pool := galleryPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, integerIdentifierDDL); err != nil {
		t.Fatalf("fixture DDL: %v", err)
	}

	parentID := postgres.IntegerColumn("id")
	parentTitle := postgres.StringColumn("title")
	parentSrc := postgres.NewTable("public", "integer_identifier_parents", "", parentID, parentTitle)

	localeParent := postgres.IntegerColumn("parent_id")
	localeKey := postgres.StringColumn("locale")
	localeBio := postgres.StringColumn("bio")
	localeSrc := postgres.NewTable("public", "integer_identifier_locales", "", localeParent, localeKey, localeBio)

	valueID := postgres.IntegerColumn("id")
	valueParent := postgres.IntegerColumn("parent_id")
	valueOrder := postgres.IntegerColumn("position")
	valueCol := postgres.StringColumn("value")
	valueSrc := postgres.NewTable("public", "integer_identifier_values", "", valueID, valueParent, valueOrder, valueCol)

	childID := postgres.IntegerColumn("id")
	childParent := postgres.IntegerColumn("parent_id")
	childOrder := postgres.IntegerColumn("position")
	childTitle := postgres.StringColumn("title")
	childSrc := postgres.NewTable("public", "integer_identifier_children", "", childID, childParent, childOrder, childTitle)
	childDecl := decl.Set[Column]{
		{Name: "id", Col: ReadOnly(childID)},
		{Name: "title", Col: Text(childTitle)},
	}
	childTable := ChildTable{
		Src: childSrc, ID: childID, Parent: childParent, Order: childOrder,
		IDType: "bigint", ParentType: "bigint", NewID: func() string { return "9007199254740995" },
	}

	relID := postgres.IntegerColumn("id")
	relParent := postgres.IntegerColumn("parent_id")
	relPath := postgres.StringColumn("path")
	relOrder := postgres.IntegerColumn("position")
	relTarget := postgres.IntegerColumn("target_id")
	relSrc := postgres.NewTable("public", "integer_identifier_rels", "", relID, relParent, relPath, relOrder, relTarget)

	d := decl.Set[Column]{
		{Name: "id", Col: ReadOnly(parentID)},
		{Name: "title", Col: Text(parentTitle)},
		{Name: "bio", Col: KeyedStrings(KeyedTable{
			Src: localeSrc, Parent: localeParent, Key: localeKey, ParentType: "bigint",
		}, localeBio, ClearSetNull())},
		{Name: "values", Col: ChildValues(ChildTable{
			Src: valueSrc, ID: valueID, Parent: valueParent, Order: valueOrder, ParentType: "bigint",
		}, Text(valueCol))},
		{Name: "children", Col: ChildRows(childTable, childDecl)},
		{Name: "related", Col: Rels(RelsTable{
			Src: relSrc, ID: relID, Parent: relParent, Path: relPath, Order: relOrder,
			ParentType: "bigint", TargetType: "bigint",
		}, "related", relTarget)},
	}
	store := Store{
		DB: pool, Table: NewTable(d, parentSrc, parentID),
		Create: func(ctx context.Context, tx pgx.Tx, _ map[string]any) (string, []editrig.Change, error) {
			_, err := tx.Exec(ctx, `INSERT INTO integer_identifier_parents (id) VALUES ($1::bigint)`, preciseBigID)
			return preciseBigID, nil, err
		},
	}

	const targetID = "9007199254740997"
	if _, err := pool.Exec(ctx, `INSERT INTO integer_identifier_targets (id) VALUES ($1::bigint)`, targetID); err != nil {
		t.Fatalf("insert target: %v", err)
	}
	outID, _, err := store.Save(ctx, nil, map[string]any{
		"title":    "parent",
		"bio":      map[string]any{"en": "hello"},
		"values":   []any{"one", "two"},
		"children": []any{map[string]any{"title": "child"}},
		"related":  []any{targetID},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if outID != preciseBigID {
		t.Fatalf("created id = %q, want %q", outID, preciseBigID)
	}

	got, err := store.Load(ctx, preciseBigID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	assertExactIdentifierFixture(t, got, targetID)

	children := got["children"].([]any)
	child := children[0].(map[string]any)
	child["title"] = "changed"
	if _, _, err := store.Save(ctx, &outID, map[string]any{"children": children}); err != nil {
		t.Fatalf("update child: %v", err)
	}
	var count int
	var title string
	if err := pool.QueryRow(ctx, `SELECT count(*), max(title) FROM integer_identifier_children WHERE parent_id = $1::bigint`, preciseBigID).Scan(&count, &title); err != nil {
		t.Fatalf("verify child: %v", err)
	}
	if count != 1 || title != "changed" {
		t.Errorf("child rows = %d, title = %q, want one preserved row with changed title", count, title)
	}
}

func assertExactIdentifierFixture(t *testing.T, got map[string]any, targetID string) {
	t.Helper()
	if got["id"] != preciseBigID {
		t.Errorf("parent id = %#v, want exact string %q", got["id"], preciseBigID)
	}
	children, _ := got["children"].([]any)
	if len(children) != 1 {
		t.Fatalf("children = %#v, want one row", got["children"])
	}
	child, _ := children[0].(map[string]any)
	if child["id"] != "9007199254740995" {
		t.Errorf("child id = %#v, want exact string", child["id"])
	}
	if fmt.Sprint(got["related"]) != "["+targetID+"]" {
		t.Errorf("related = %#v, want exact string target", got["related"])
	}
	if fmt.Sprint(got["bio"]) != "map[en:hello]" || fmt.Sprint(got["values"]) != "[one two]" {
		t.Errorf("side tables did not round-trip: bio=%#v values=%#v", got["bio"], got["values"])
	}
}
