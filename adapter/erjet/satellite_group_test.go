package erjet_test

import (
	"strings"
	"testing"

	"github.com/go-jet/jet/v2/postgres"

	"github.com/qrotux/editrig-go/adapter/erjet"
)

// TestKeyedUpsertManyCarriesEveryColumnInOneStatement pins the SQL shape
// writeMany exists for: one INSERT ... ON CONFLICT DO UPDATE carrying every
// passed column, not one query per column.
//
// Postgres checks NOT NULL on the tentative INSERT row before resolving ON
// CONFLICT: a NOT NULL column missing from the VALUES list violates the
// constraint even when the conflicting row already carries it. The test does
// not hit a database, so it pins the SQL shape instead: title must be in the
// same INSERT as description, or the live repro (TestSatelliteGroupWrite* in
// conformance_test.go) stays red with a green unit.
func TestKeyedUpsertManyCarriesEveryColumnInOneStatement(t *testing.T) {
	parentCol := postgres.StringColumn("parent_id")
	keyCol := postgres.StringColumn("locale")
	titleCol := postgres.StringColumn("title")
	descCol := postgres.StringColumn("description")
	kt := erjet.KeyedTable{
		Src: postgres.NewTable("public", "trips_locales", "",
			parentCol, keyCol, titleCol, descCol),
		Parent:     parentCol,
		Key:        keyCol,
		ParentType: "uuid",
		KeyType:    "public._locales",
	}

	sql, _ := erjet.ExportKeyedUpsertManySQL(kt, "p-1", "ru",
		[]postgres.ColumnString{titleCol, descCol},
		[]erjet.ClearPolicy{erjet.ClearSet(postgres.String("")), erjet.ClearSetNull()},
		[]map[string]any{{"ru": "Заголовок"}, {"ru": "Описание"}})

	if strings.Count(sql, "INSERT INTO") != 1 {
		t.Fatalf("upsertManySQL produced %d INSERT statements, want exactly 1:\n%s",
			strings.Count(sql, "INSERT INTO"), sql)
	}
	if !strings.Contains(sql, "title") || !strings.Contains(sql, "description") {
		t.Errorf("SQL does not carry both columns in one statement: %s", sql)
	}
	if !strings.Contains(sql, "ON CONFLICT") || !strings.Contains(sql, "DO UPDATE") {
		t.Errorf("SQL is not an upsert: %s", sql)
	}
}

// TestKeyedUpsertManySkipsColumnsAbsentForTheKey pins that "not sent means
// do not touch" holds inside the shared upsert too: a column with no patch for
// this key at all (another locale edited by a neighbouring cell) appears in
// neither VALUES nor SET.
func TestKeyedUpsertManySkipsColumnsAbsentForTheKey(t *testing.T) {
	parentCol := postgres.StringColumn("parent_id")
	keyCol := postgres.StringColumn("locale")
	titleCol := postgres.StringColumn("title")
	descCol := postgres.StringColumn("description")
	kt := erjet.KeyedTable{
		Src: postgres.NewTable("public", "trips_locales", "",
			parentCol, keyCol, titleCol, descCol),
		Parent: parentCol,
		Key:    keyCol,
	}

	// title carries a patch only for "en"; for key "ru" it is absent.
	sql, _ := erjet.ExportKeyedUpsertManySQL(kt, "p-1", "ru",
		[]postgres.ColumnString{titleCol, descCol},
		[]erjet.ClearPolicy{erjet.ClearSet(postgres.String("")), erjet.ClearSetNull()},
		[]map[string]any{{"en": "Title"}, {"ru": "Описание"}})

	if strings.Contains(sql, "title") {
		t.Errorf("column absent from the payload for this key must not appear in the statement: %s", sql)
	}
	if !strings.Contains(sql, "description") {
		t.Errorf("the column actually present for this key must still be written: %s", sql)
	}
}
