// Command postgres runs an editor backed by a local PostgreSQL database.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	editrig "github.com/qrotux/editrig-go"
	"github.com/qrotux/editrig-go/adapter/erjet"
	"github.com/qrotux/editrig-go/decl"
	"github.com/qrotux/editrig-go/router/erstd"
	"github.com/qrotux/editrig-go/ui"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("Set DATABASE_URL to a local example database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	if err := setup(ctx, pool); err != nil {
		log.Fatal(err)
	}
	log.Print("Editor API: http://127.0.0.1:8080/articles/schema")
	log.Fatal(http.ListenAndServe("127.0.0.1:8080", newHandler(pool)))
}

func setup(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS editrig_example_articles (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 title text NOT NULL,
 note text
 )`)
	return err
}

func newHandler(pool *pgxpool.Pool) http.Handler {
	id := postgres.IntegerColumn("id")
	title := postgres.StringColumn("title")
	note := postgres.StringColumn("note")
	table := postgres.NewTable("public", "editrig_example_articles", "", id, title, note)
	fields := decl.Set[erjet.Column]{
		{Name: "id", Col: erjet.ReadOnly(id), UI: ui.String().Readonly().Hidden()},
		{Name: "title", Col: erjet.Text(title), UI: ui.String().Required().MinLen(1)},
		{Name: "note", Col: erjet.Text(note), UI: ui.String().Nullable()},
	}
	store := erjet.Store{DB: pool, Table: erjet.NewTable(fields, table, id), Create: func(ctx context.Context, tx pgx.Tx, in map[string]any) (string, []editrig.Change, error) {
		var key int64
		err := tx.QueryRow(ctx, "INSERT INTO editrig_example_articles (title) VALUES ($1) RETURNING id", in["title"]).Scan(&key)
		if err != nil {
			return "", nil, err
		}
		return strconv.FormatInt(key, 10), nil, nil
	}}
	entity := editrig.Entity{Name: "articles", Load: store.Load, Save: store.Save, Delete: store.Delete,
		Schema: func(_ context.Context, cat editrig.Catalog, _ map[string]any) (json.RawMessage, json.RawMessage, error) {
			return (ui.Document{Fields: fields.Fields(cat.Title, cat.Label), Order: fields.Order()}).Marshal()
		},
	}
	reg, err := editrig.NewRegistry(entity)
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	erstd.Register(mux, "", nil, editrig.NewHandler(reg, nil, nil))
	return mux
}
