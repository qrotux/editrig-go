package erjet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixture: a parent, a media table and a child gallery referencing both.
// Built by DDL on an ephemeral database, with the go-jet description written
// by hand: the library has no generated code, and the test doubles as proof
// that erjet does not depend on codegen.
const galleryDDL = `
CREATE TABLE IF NOT EXISTS deferred_parents (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid()
);
CREATE TABLE IF NOT EXISTS deferred_media (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid()
);
CREATE TABLE IF NOT EXISTS deferred_gallery (
	id         varchar NOT NULL PRIMARY KEY,
	_parent_id uuid NOT NULL REFERENCES deferred_parents(id) ON DELETE CASCADE,
	_order     integer NOT NULL,
	image_id   uuid NOT NULL REFERENCES deferred_media(id)
)`

var (
	galleryIDCol     = postgres.StringColumn("id")
	galleryParentCol = postgres.StringColumn("_parent_id")
	galleryOrderCol  = postgres.IntegerColumn("_order")
	galleryImageCol  = postgres.StringColumn("image_id")
	galleryTable     = postgres.NewTable("public", "deferred_gallery", "",
		galleryIDCol, galleryParentCol, galleryOrderCol, galleryImageCol)
	parentCoverCol = postgres.StringColumn("cover_id")
)

var galleryChildForTest = ChildTable{
	Src: galleryTable, ID: galleryIDCol,
	Parent: galleryParentCol, Order: galleryOrderCol,
	ParentType: "uuid", NewID: newGalleryTestID,
}

func newGalleryTestID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic("deferred_list_int_test: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// deferredListCell is the gallery cell over the child table: the test must
// take the same path as a live Save.
func deferredListCell(resolve DeferredFunc) Column {
	return DeferredList(ChildValues(galleryChildForTest, UUID(galleryImageCol)), resolve)
}

// Elements materialize before the list is written and in the same
// transaction: a media element gets its id only after the media row's INSERT.
func TestDeferredListMaterializesEveryItem(t *testing.T) {
	pool := galleryPool(t)
	parent := insertGalleryParent(t, pool)
	first, second := insertMediaRow(t, pool), insertMediaRow(t, pool)
	// The resolver swaps the marker for a real id, the way the application
	// swaps a deferred file for the id of the media row it just created.
	cell := deferredListCell(func(_ context.Context, _ pgx.Tx, _ string, v any) (any, error) {
		if v == "new" {
			return second, nil
		}
		return v, nil
	})

	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := cell.save(context.Background(), tx, parent, []any{first, "new"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	got := galleryImageIDs(t, tx, parent)
	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Errorf("gallery = %v, want [%s %s]", got, first, second)
	}
}

// An element the resolver did not parse (nil) is skipped, but the positions
// of the rest stay contiguous - otherwise ordering silently gets holes.
func TestDeferredListSkipsUnresolvedItems(t *testing.T) {
	pool := galleryPool(t)
	parent := insertGalleryParent(t, pool)
	first, second := insertMediaRow(t, pool), insertMediaRow(t, pool)
	cell := deferredListCell(func(_ context.Context, _ pgx.Tx, _ string, v any) (any, error) {
		if v == "junk" {
			return nil, nil
		}
		return v, nil
	})

	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := cell.save(context.Background(), tx, parent, []any{first, "junk", second}); err != nil {
		t.Fatalf("save: %v", err)
	}

	if got := galleryOrders(t, tx, parent); len(got) != 2 || got[0]+1 != got[1] {
		t.Errorf("orders = %v, want two consecutive positions", got)
	}
}

// A resolver error fails the whole Save: a half-written list is worse than a
// refusal - the user would see "saved" on an incomplete gallery.
func TestDeferredListPropagatesResolverError(t *testing.T) {
	pool := galleryPool(t)
	parent := insertGalleryParent(t, pool)
	want := errors.New("persist failed")
	cell := deferredListCell(func(context.Context, pgx.Tx, string, any) (any, error) { return nil, want })

	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := cell.save(context.Background(), tx, parent, []any{"anything"}); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if got := galleryImageIDs(t, tx, parent); len(got) != 0 {
		t.Errorf("gallery = %v, want nothing written", got)
	}
}

// Declaration gates: the combinator refuses a cell without a second phase and
// a cell of non-list shape - both holes would otherwise surface only on a
// live form.
func TestDeferredListPanicsOnUnsuitableCell(t *testing.T) {
	pass := func(_ context.Context, _ pgx.Tx, _ string, v any) (any, error) { return v, nil }
	for _, tc := range []struct {
		name string
		cell Column
	}{
		{"no second phase", UUID(galleryImageCol)},
		{"not a list", Deferred(parentCoverCol, pass)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("DeferredList did not panic")
				}
			}()
			DeferredList(tc.cell, pass)
		})
	}
}

// --- helpers -----------------------------------------------------------------

// galleryPool opens EDITRIG_TEST_DATABASE_URL and creates the fixture. The
// test skips without the variable; DDL is allowed only on an explicitly
// ephemeral host, the same rule as ephemeralPool in conformance_test.go.
func galleryPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("EDITRIG_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("EDITRIG_TEST_DATABASE_URL is not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("EDITRIG_TEST_DATABASE_URL is not a URL: %v", err)
	}
	host := strings.ToLower(u.Hostname())
	ephemeral := host == "localhost" || host == "127.0.0.1" || host == "::1"
	for _, s := range []string{"testdb", "throwaway", "ephemeral"} {
		ephemeral = ephemeral || strings.Contains(host, s)
	}
	if !ephemeral {
		t.Skipf("host %q does not look ephemeral - refusing to run DDL against it", host)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, galleryDDL); err != nil {
		t.Fatalf("fixture DDL: %v", err)
	}
	return pool
}

func insertGalleryParent(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO deferred_parents DEFAULT VALUES RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("insert parent: %v", err)
	}
	return id
}

func insertMediaRow(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO deferred_media DEFAULT VALUES RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	return id
}

// galleryImageIDs returns the image_id of the parent's gallery rows in
// position order, read on the same transaction as the write: the check runs
// before commit.
func galleryImageIDs(t *testing.T, tx pgx.Tx, parent string) []string {
	t.Helper()
	rows, err := tx.Query(context.Background(),
		`SELECT image_id::text FROM deferred_gallery WHERE _parent_id = $1::uuid ORDER BY _order`, parent)
	if err != nil {
		t.Fatalf("select gallery: %v", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan image_id: %v", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// galleryOrders returns the positions of the parent's gallery rows, ascending.
func galleryOrders(t *testing.T, tx pgx.Tx, parent string) []int {
	t.Helper()
	rows, err := tx.Query(context.Background(),
		`SELECT _order FROM deferred_gallery WHERE _parent_id = $1::uuid ORDER BY _order`, parent)
	if err != nil {
		t.Fatalf("select orders: %v", err)
	}
	defer rows.Close()
	out := []int{}
	for rows.Next() {
		var pos int
		if err := rows.Scan(&pos); err != nil {
			t.Fatalf("scan _order: %v", err)
		}
		out = append(out, pos)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}
