# editrig-go

Server side of a server-driven entity editor. An entity is declared once in Go — fields, sections, storage cells — and the library publishes it to the client as a JSON Schema + uiSchema envelope and serves load, create, update, delete, options and file upload over HTTP. The matching React UI is [`@qrotux/editrig-shadcn-react`](https://github.com/qrotux/editrig-shadcn-react) (rjsf v6, shadcn/ui).

The root package `editrig` is the protocol: HTTP, schema envelope, validation, read-only stripping, the registry. It knows nothing about SQL, drivers, locales or audit logs. Those are seams:

| Seam | Type | Shipped implementation |
|---|---|---|
| Storage | `Entity.Load` / `Save` / `Delete` hooks | `adapter/erjet` ([go-jet](https://github.com/go-jet/jet) + [pgx](https://github.com/jackc/pgx) v5, Postgres) |
| Localized copy | `editrig.Catalog`, built per request by `editrig.Copy` | none — the application implements it |
| Relation option lists | `editrig.OptionSource` | none — the application implements it |
| Storage conformance | `storetest.Run` | runs against any implementation |
| HTTP routing | `editrig.Handler` methods | `router/erchi` ([chi](https://github.com/go-chi/chi) v5), `router/erstd` (net/http ServeMux) |

## Install

```
go get github.com/qrotux/editrig-go
```

## Wiring

```go
import (
    "log/slog"

    "github.com/go-chi/chi/v5"

    "github.com/qrotux/editrig-go"
    "github.com/qrotux/editrig-go/router/erchi"
)

reg, err := editrig.NewRegistry(articles, users)
if err != nil {
    log.Fatal(err) // invalid entities fail at startup, not per request
}

h := editrig.NewHandler(reg, copyFor, slog.Default())

r := chi.NewRouter()
erchi.Register(r, "/api/admin/entities", adminGuard, h)
```

`erstd.Register(mux, "/api/admin/entities", adminGuard, h)` does the same on an `*http.ServeMux` (Go 1.22 patterns). Both are **reference mounts for the default URL shape**, six routes under `base`:

| method | path | handler |
|---|---|---|
| GET | `{name}/schema` | `Schema` |
| GET | `{name}/options/{field}` | `Options` |
| GET | `{name}/{id}` | `Load` |
| POST | `{name}` | `Create` |
| PATCH | `{name}/{id}` | `Update` |
| DELETE | `{name}/{id}` | `Delete` |

`guard` (a `func(http.Handler) http.Handler`, may be nil) wraps every route the same way. That is all `Register` can express: it decides the path shape and wraps reads and writes identically. Both decisions belong to the application, so the **canonical way to mount is to call the `Handler` methods yourself**. The core knows no router: each method takes its route parameters explicitly — `h.Load(w, r, name, id)`, `h.Options(w, r, name, field)` — and reads nothing from the URL, so any mux mounts it in six lines by extracting the params its own way. Use `Register` when the default shape and a uniform guard are exactly what you need; write the six lines when they are not, for example a client that expects an `entity` segment between the name and the id, or an audit wrapper that must sit around writes only:

```go
p := chi.URLParam
r.Route("/api/admin/entities", func(r chi.Router) {
    r.Use(adminGuard)
    r.Get("/{name}/schema", func(w http.ResponseWriter, r *http.Request) {
        h.Schema(w, r, p(r, "name"))
    })
    r.Get("/{name}/options/{field}", func(w http.ResponseWriter, r *http.Request) {
        h.Options(w, r, p(r, "name"), p(r, "field"))
    })
    r.Get("/{name}/entity/{id}", func(w http.ResponseWriter, r *http.Request) {
        h.Load(w, r, p(r, "name"), p(r, "id"))
    })
    r.Group(func(r chi.Router) {
        r.Use(audit.MountWrite) // the application's: actor check, journal row after a 2xx
        r.Post("/{name}/entity", func(w http.ResponseWriter, r *http.Request) {
            h.Create(w, r, p(r, "name"))
        })
        r.Patch("/{name}/entity/{id}", func(w http.ResponseWriter, r *http.Request) {
            h.Update(w, r, p(r, "name"), p(r, "id"))
        })
        r.Delete("/{name}/entity/{id}", func(w http.ResponseWriter, r *http.Request) {
            h.Delete(w, r, p(r, "name"), p(r, "id"))
        })
    })
})
```

The path shape is the client's contract, not the library's; the only thing the library fixes is that `Schema`, `Load` and `Options` are the reading half and `Create`, `Update` and `Delete` the writing half.

`copyFor` and the logger may be `nil`: a nil `Copy` yields `PlainCatalog{}` (keys rendered as-is, no localization), a nil logger means `slog.Default()`.

### Handler options

`NewHandler` takes optional `HandlerOption` values after its three arguments:

```go
h := editrig.NewHandler(reg, nil, nil,
    editrig.WithUnknownFields(editrig.RejectUnknownFields),  // default: AllowUnknownFields
    editrig.WithBodyLimits(editrig.BodyLimits{JSONBytes: 1 << 20}),
)
```

| option | what it does |
|---|---|
| `WithUnknownFields(policy)` | what happens to a submitted top-level key that is not in the schema's `properties`. `AllowUnknownFields` (default) passes it through to `Validate`/`Save`; `RejectUnknownFields` answers 422 with `"unknown field"` per key; `StripUnknownFields` deletes it silently. Nested objects are governed by the JSON Schema itself (`additionalProperties`), not by this policy. An unknown value panics. |
| `WithBodyLimits(BodyLimits{JSONBytes, MultipartBytes, MultipartMemory})` | request body bounds for the write handlers. Zero means the default: 4 MiB JSON, 128 MiB multipart, 4 MiB multipart memory threshold. A negative value panics. See [File upload](#file-upload). |

Options apply to the whole handler; there is no per-entity configuration.

`copyFor` is an `editrig.Copy`: `func(r *http.Request, entity string) Catalog`. The engine passes the entity name from `Entity.Name` and nothing else — the locale axis (session, header, tenant, brand) is entirely the application's, and it never appears in a library signature. See [i18n](#i18n).

An `Entity` is a record of hooks:

```go
type Entity struct {
    Name     string
    DisableCreate, DisableUpdate, DisableDelete bool
    Schema   func(ctx context.Context, cat Catalog, data map[string]any) (schema, uiSchema json.RawMessage, err error)
    Load     func(ctx context.Context, id string) (map[string]any, error)
    Save     func(ctx context.Context, id *string, in map[string]any) (outID string, ferr []FieldError, err error)
    Delete   func(ctx context.Context, id string) error
    Validate func(ctx context.Context, cat Catalog, id *string, in map[string]any) []FieldError
    Options  map[string]OptionSource
}
```

`NewRegistry` rejects an empty name, a duplicate name, and a nil `Schema` or `Load`. Everything else is optional: a nil `Validate` skips business validation; a nil `Save` makes `Create` and `Update` answer **405**, a nil `Delete` does the same for `Delete`. The `Disable*` flags restrict individual operations of an entity that does have the hook — `DisableCreate` for an update-only editor, `DisableUpdate` for append-only data — with the same 405.

Contracts worth knowing before you implement them:

- `Schema(ctx, cat, nil)` builds the **create** form; `Schema(ctx, cat, data)` builds the **edit** form and may depend on the row's current values.
- `Load` reports "no such row" as `(nil, nil)` — not an error. The engine answers 404.
- `Save` reports "no such row" as `editrig.ErrNotFound` (→ 404) and business validation failures as `ferr` (→ 422). **Atomicity is the entity's**: the engine never opens a transaction and has no driver type in any signature, so a `Save` that returns `ferr` or `err` must have rolled back its own work.
- `Validate` runs after structural (JSON Schema) validation and before `Save`. It is the only hook with both a catalog and a position before the transaction, so expensive preparation of a value belongs there. It **may mutate `in`**.

## Declaring an entity

Three packages collaborate. `ui` is the presentation vocabulary (a leaf package, no imports), `adapter/erjet` is the persistence vocabulary, and `decl` is where a field's two halves sit side by side in one record — so the form and the column cannot drift apart.

```go
import (
    "github.com/go-jet/jet/v2/postgres"

    "github.com/qrotux/editrig-go/decl"
    "github.com/qrotux/editrig-go/adapter/erjet"
    "github.com/qrotux/editrig-go/ui"
)

var groups = []ui.Group{
    {ID: "identity", Title: "Identity", Columns: 2},
    {ID: "content", Title: "Content"},
}

var articles = decl.Set[erjet.Column]{
    {Name: "id", Col: erjet.ReadOnly(table.Articles.ID), UI: ui.String().Hidden().Readonly()},
    {Name: "slug", Group: "identity", Col: erjet.Str(table.Articles.Slug, ""), UI: ui.String().Required().MaxLen(80)},
    {Name: "status", Group: "identity", Col: erjet.Str(table.Articles.Status, "draft"), UI: ui.Enum("draft", "published")},
    {Name: "views", Group: "identity", Col: erjet.ReadOnly(table.Articles.Views), UI: ui.Number().Readonly()},
    {Name: "published_at", Group: "identity", Col: erjet.Time(table.Articles.PublishedAt), UI: ui.Timestamp().Nullable().Widget("datetime")},

    // One field, one value per key, one object on the wire: {"en": "...", "ru": "..."}.
    {Name: "title", Group: "content", Col: erjet.KeyedStrings(articleLocales, table.ArticlesLocales.Title, erjet.ClearSetNull()),
        UI: ui.Keyed(ui.String(), ui.Keys("en", "ru"))},
    {Name: "tags", Group: "content", Col: erjet.Strings(table.Articles.Tags), UI: ui.List(ui.String()).Nullable()},
    {Name: "author_id", Group: "content", Col: erjet.UUID(table.Articles.AuthorID), UI: ui.Relation("users", false)},
    {Name: "cover_id", Group: "content", Col: erjet.Deferred(table.Articles.CoverID, saveCover), UI: ui.Media("covers", "3:2")},

    // A repeatable block: one row per item in a child table, the element declared
    // by the same Set type — so there is no second vocabulary for item fields.
    {Name: "sections", Group: "content", Col: erjet.ChildRows(sectionsChild, sectionDecl), Items: sectionDecl, UI: ui.Items()},
}
```

The slice order is the single source of order: it drives `ui:order`, the order of SELECT projections and the order of SET assignments. `Entry.Group` points at a section from the registry — the section does not list its fields, so a field name is written exactly once.

`decl.Set` never calls a method on its cell type, so the declaration itself is storage-agnostic; the constraint that a cell can answer `Writable()` and `Accepts(ui.Wire)` is declared on `decl.Lint`, not on `Set`.

### From declaration to schema

```go
func articleSchema(ctx context.Context, cat editrig.Catalog, data map[string]any) (json.RawMessage, json.RawMessage, error) {
    doc := ui.Document{
        Fields: articles.Fields(cat.Title, cat.Label),
        Order:  articles.Order(),
        Groups: articles.Groups(groups),
        Header: &ui.Header{TitleField: "slug", MetaFields: []string{"id", "published_at"}},
    }
    return doc.Marshal()
}
```

`Set.Fields` is the presentation projection: it fills in every title and every enum/key label from the catalog functions, recursing into `Entry.Items` (with keys `<field>_fields.<sub>`). `Document.Marshal` splits each `ui.Field` by the `ui:` prefix — `ui:` keys become the field's uiSchema entry, everything else becomes its JSON Schema property — and assembles the root `required`, `ui:order`, `ui:groups` and `ui:header`. `Set.Groups` drops sections no field references and warns (once per request) about a field pointing at an undeclared section, rendering it outside any section rather than failing the form.

### From declaration to storage

```go
table := erjet.NewTable(articles, table.Articles, table.Articles.ID)

store := erjet.Store{
    DB:      pool, // *pgxpool.Pool satisfies erjet.DB structurally
    Table:   table,
    Touch:   table.Articles.UpdatedAt,
    Create:  createArticle, // func(ctx, tx, in) (id string, []editrig.Change, error)
    OnWrite: func(ctx context.Context, id string, ch []editrig.Change, created bool) {
        audit.Record(ctx, id, ch, created) // the application's journal, not the library's
    },
}

article := editrig.Entity{
    Name:     "articles",
    Schema:   articleSchema,
    Load:     store.Load,
    Save:     store.Save,
    Delete:   store.Delete,
    Validate: validateArticle,
    Options:  map[string]editrig.OptionSource{"author_id": authorOptions},
}
```

The primary key may be a text, uuid or integer column (`bigint GENERATED ALWAYS AS IDENTITY` works). Identifiers stay **strings on the wire** whatever the column type: an integer id is read back as its exact decimal string, never through `float64`, so a bigint above 2^53 keeps its precision, and the same applies to child, parent and relation ids. Lookups compare through `::text`, so a malformed `{id}` is a 404, not a cast error.

`Store.Load`/`Save`/`Delete` match the `Entity` hook signatures exactly, so they go in without an adapter. `Save` opens the transaction (the engine cannot — it has no driver type), builds a **partial** UPDATE from the intersection of the payload with the writable cells of the declaration — a key absent from the payload leaves its column untouched, an explicit JSON `null` becomes an explicit SQL NULL — and calls `OnWrite` only after a successful commit and only when something actually changed. The only knowledge left to the entity is `Create`: which columns an insert fills.

`OnWrite` reports `[]editrig.Change{{Field, Old, New}}` in wire form. The store computes the diff (only it holds both states) and reports it in a **core** type, so the application writes one bridge to its journal regardless of which store it uses. The library itself never writes an audit log.

### Cells

`adapter/erjet` cells, all `erjet.Column`:

| Kind | Constructors |
|---|---|
| Scalars | `Str`, `Text`, `UUID`, `Num`, `Int`, `Bool`, `Time`, `TimeLocal`, `TimeOfDay`, `Date` |
| Arrays | `Strings`, `Ints`, `Floats`, `Timestamps` |
| Documents | `JSON` (read-only), `JSONEditable` |
| Side table, one row per (parent, key) | `KeyedStrings` with a `ClearPolicy`: `ClearSetNull`, `ClearSet`, `ClearDeleteRow` |
| Ordered list of foreign ids | `Rels` over a `RelsTable` |
| Child table, list of scalars | `ChildValues`, `KeyedChildValues` |
| Child table, list of objects | `ChildRows`, `KeyedChildRows` (the element is a nested `decl.Set`) |
| Second-phase writes | `Deferred`, `DeferredList` — the value only exists once the parent row does (an uploaded image id) |
| Read-only / frozen | `ReadOnly`, `Frozen` |

Two rules worth repeating, because both fail silently otherwise:

- **`ChildValues` replaces (DELETE + INSERT), `ChildRows` upserts by id.** Use `ChildValues` only when nothing references the child table's id; anything with an `ON DELETE CASCADE` child of its own must use `ChildRows`, or every save would quietly drop the nested rows.
- **The `Key` column decides the constructor.** `ChildValues`/`ChildRows` require `ChildTable.Key == nil`; `KeyedChildValues`/`KeyedChildRows` require it non-nil. Both check at declaration time and panic, as does `ChildTable.requireColumns` for a missing `ID`, `Parent` or `Order`.

### Linting the declaration

`decl.Lint(articles, groups)` returns a list of problems — run it from a test of the entity, not at runtime: every finding is a defect of the declaration, permanently present or permanently absent. It checks, among other things, that a cell's writability agrees with `.Readonly()` in both directions (a read-only cell under an editable input; a writable cell under a field the engine will strip before `Save`), that a required field is writable and not read-only, that the field's declared wire shape (`ui.Field.WireKind`) is one the cell can parse (`Column.Accepts`), that `Entry.Items` is paired with `ui.Items()` or `ui.Keyed(ui.Items(), …)`, that `ui:options.parentField` names a real field, that no visible field is left out of every section and no section is empty or untitled. It recurses into `Entry.Items`, and it folds in `ui.Lint`, which catches JSON Schema keywords that are meaningless for a field's type (`ui.Bool().MaxLen(50)` — which no validator will ever complain about, by spec).

## Wire protocol

### Envelope

`GET <base>/{name}/schema`, `GET <base>/{name}/{id}` and the bodies of `POST`/`PATCH` responses all return the same shape:

```json
{
  "name": "articles",
  "id": "7c3f…",
  "schema": {"type": "object", "required": ["slug"], "properties": {"…": {}}},
  "uiSchema": {"ui:order": ["…"], "ui:groups": [], "slug": {"ui:placeholder": "…"}},
  "data": {"slug": "hello", "tags": ["go"]}
}
```

On `GET /schema` (the create form) `id` is `null` and `data` is `{}`. Everywhere else `id` is the row id and `data` is what `Entity.Load` returned. Create answers **201**, update **200**, delete **204** with no body.

### Request body

`POST` and `PATCH` take `{"data": {…}}`. Update is a **partial** update as far as the shipped store is concerned: a key absent from `data` is not written, an explicit `null` is.

Before anything else happens, the engine strips every top-level key the uiSchema marked `"ui:readonly": true` — so `Entity.Save` physically never sees a read-only key, and a stale or forged form cannot overwrite a value the admin does not own. Only the top level is stripped; a read-only id inside a repeatable item does reach the store, which is why `ChildRows` re-checks item ids against the ones the parent actually owns.

Then the unknown-field policy runs (see [Handler options](#handler-options)), and then, in this order: JSON Schema validation → file injection → `Entity.Validate` → `Entity.Save`. The structural half lives in the `validate` package (`validate.Structural`, `validate.Write`, `validate.UnknownFields`, `validate.StripReadonly`, `validate.MediaFieldKind`) and can be called outside HTTP, for an import job or an entity test.

**What structural validation sees on `PATCH` is the effective state, not the payload.** A partial update that omits a required field must not fail `required`, and one that sets a nullable field to `null` must be checked as `null`. So the engine validates the row's current values overlaid with the submitted top-level keys: a submitted key replaces the current value, including objects and arrays, except a `"ui:field": "keyed"` object, whose submitted keys are merged into the current map one level deep — so `{"title": {"ru": "…"}}` is validated with the `en` translation still present. `Validate` and `Save` still receive **only the submitted payload**: the overlay exists for the validator and nowhere else. Values loaded from the store are re-encoded through JSON before the overlay, with numbers kept as `json.Number`, so a bigint id survives exactly.

### Errors

Validation failures — structural or from `Validate` or from `Save` — answer **422** with:

```json
{"fieldErrors": [{"field": "/slug", "message": "<validator message>"}], "formErrors": []}
```

`field` is a JSON Pointer into the submitted data; a `FieldError` with an empty `Field` becomes an entry in `formErrors` instead. Both arrays are always present, never `null`. A `required` failure reports one entry per missing property, pointing at the property (`/slug`), not at the containing object. Under `RejectUnknownFields`, each undeclared top-level key is one entry with the message `"unknown field"`, and the request stops there, before JSON Schema validation.

A `*editrig.ConflictError` returned from `Entity.Delete` answers **409** with the same body, the rendered message in `formErrors`. The error carries catalog *keys* and counts, not a formatted string — `Delete` has no catalog in its signature, so the handler renders it: `Message(Key)`, then `", "`-joined `Message(ref.LabelKey) + " " + count`.

Everything else is `{"error": "<word>"}`:

| status | body | when |
|---|---|---|
| 400 | `{"error":"unknown entity"}` | `{name}` is not registered |
| 400 | *(decoder message)* | malformed JSON body |
| 400 | `{"error":"malformed multipart body"}` | broken boundary, truncated part |
| 404 | `{"error":"not found"}` | `Load` returned `(nil, nil)`, `Save` returned `ErrNotFound`, or the options field has no source |
| 405 | `{"error":"method not allowed"}` | the entity has no `Save`/`Delete`, or the operation is disabled by a `Disable*` flag |
| 413 | `{"error":"body too large"}` | body over the configured limit (default 4 MiB JSON, 128 MiB multipart) |
| 500 | `{"error":"load"\|"save"\|"delete"\|"schema"\|"options"}` | infrastructure failure; the detail goes to the `*slog.Logger` |

### File upload

A submit carrying files is `multipart/*` instead of JSON:

- part **`payload`** — the same `{"data": {…}}` JSON as the plain body. Absent or empty ⇒ the input is `{}`.
- part **`file:<field>`** — one part per file, named after the form field. A part with any other name is ignored.

The engine reads the bytes into `editrig.Upload{Filename, Mime, Data}` and puts it into the payload *in place of the field's value*, then stops: what to do with the bytes is the entity's business (it owns the category, the size policy and the ownership rules). The core neither decodes images nor writes to storage.

Whether a field accepts files is read from its uiSchema entry: `"ui:field": "media"`, and `"ui:options".multi` decides single or multiple. A single-valued media field takes one `Upload`; a multi one appends the new uploads **after** the ids already in `data[field]`, so the array itself expresses order and deletion of existing items. A file part on a non-media field, on a read-only field, or a second part on a single-valued field is a `FieldError` (`"unexpected file"`), not a silent drop — a lost file must not look like a successful save.

Structural validation runs with the uploads in place: a required media field with a pending file is not "missing", and a multi field's `minItems`/`maxItems` are checked against the final list — existing ids plus new files. The pending `Upload` is never validated as if it were an id string; internally it is represented by a marker the schema is widened to accept, and the marker never reaches `Validate` or `Save`. This widening needs the media field declared directly under the root `properties`; a root schema using `$ref`, `allOf`, `if`/`then` or similar combinators is a 500 (`"schema"`) on a submit that carries files.

Body bounds come from `WithBodyLimits`, defaults 4 MiB for a JSON body and 128 MiB for a multipart body, both enforced by `http.MaxBytesReader` and answered with 413. The multipart memory threshold (default 4 MiB) is only the memory/tempfile boundary and is **not** a size limit. Per-file limits are the entity's, in `Validate`.

### Options

`GET <base>/{name}/options/{field}` serves one relation field's values:

```
?q=ivan&limit=25              → search
?ids=7c3f…,9a11…              → hydrate the labels of already-chosen values
?parent=<id>                  → scope the list to an owner
```

```json
{"options": [{"value": "7c3f…", "label": "Ivan Petrov"}]}
```

`limit` defaults to 50 and is capped at 200; a non-numeric `limit` falls back to the default rather than answering 400. Empty `ids` elements (`a,,b`) are dropped. `options` is always an array, never `null`. An unregistered field — including an entity with no `Options` map at all — answers 404.

The `OptionSource` behind it is a closure, so it can carry a pool, a slice, a JSON file or a foreign service; the library ships none. Its contract: non-empty `IDs` means hydration (ignore `Search` and `Limit` and return exactly those values), `Limit` is already clamped, order is significant and stable, an empty result is an empty slice and not an error, a label is never empty (fall back to the id), and everything request-scoped arrives through `cat` — there is no other channel.

## i18n

The library has neither a locale nor a translator in any signature. It asks a `Catalog` — an interface the application implements — for already-localized copy, and it builds one per request through the `Copy` factory, supplying only the entity name:

```go
type Catalog interface {
    Title(field string) string
    Label(field, value string) string
    Group(id string) string
    Message(suffix string) string
}

type Copy func(r *http.Request, entity string) Catalog
```

The key convention lives in the core because three parties must agree on it — the message catalog, the coverage gate and the entity:

| what | key | helper |
|---|---|---|
| field title | `editor.<entity>.<field>` | `editrig.Key` |
| enum value / map key label | `editor.<entity>.<field>_values.<value>` | `editrig.ValueKey` |
| subfield of a repeatable item | `editor.<entity>.<field>_fields.<sub>` | `editrig.FieldKey` |
| section title | `editor.<entity>.groups.<id>` | `editrig.GroupKey` |
| conflict / domain message | `editor.<entity>.<suffix>` | `editrig.Key` |

The entity never sees a key or a prefix: the engine substitutes `Entity.Name`, so the literal that names the entity in the registry is the same literal that appears in its keys. The `_values` and `_fields` suffixes are forced by nesting — in a nested-JSON catalog, `editor.articles.status` cannot be both a string (the title) and an object (the value labels).

## Tests

```
go test ./...
EDITRIG_TEST_DATABASE_URL=postgres://user:pass@localhost:5432/db go test ./adapter/erjet/
```

The full gate, which is what CI runs:

```
test -z "$(gofmt -l .)" && go vet ./... && go test ./...
```

Everything in the root, `decl`, `ui`, `storetest` and `examples/memory` runs without a database. The Postgres-backed tests in `adapter/erjet` **skip** when `EDITRIG_TEST_DATABASE_URL` is unset, and skip again if the DSN's host does not look ephemeral (`localhost`, `127.0.0.1`, `::1`, or a host containing `testdb`, `throwaway` or `ephemeral`) — they run DDL and `TRUNCATE`, so pointing them at a dev database by accident must not be possible. `examples/postgres` follows the same rule. CI has no database service; the Postgres suite is run locally before a store change is called done, together with `go test -race ./...`.

## Examples

Two runnable programs under `examples/`, each with a test that drives the full CRUD flow over HTTP:

- `examples/memory` — one entity over a `map`, mounted with `router/erstd`, no catalog, a 1 MiB JSON limit. `go run ./examples/memory` and open `http://127.0.0.1:8080/articles/schema`.
- `examples/postgres` — the same entity declared with `decl` and stored through `erjet` on a `bigint` identity key. `DATABASE_URL=postgres://… go run ./examples/postgres`; it creates its own table.

## Writing another Store

A store is three functions over `map[string]any` plus a report of what was written. Half of the contract is not in those signatures but in behaviour: how "no such row" is reported, whether an absent key differs from an explicit `null`, what shape values come back in. `storetest` makes that contract executable, in the style of `fstest.TestFS`:

```go
func TestConformance(t *testing.T) {
    storetest.Run(t, newSubject)
}
```

`newSubject` is a `storetest.New` — a factory returning a clean store per check:

```go
type Subject struct {
    Load    func(ctx context.Context, id string) (map[string]any, error)
    Save    func(ctx context.Context, id *string, in map[string]any) (outID string, ferr []editrig.FieldError, err error)
    Delete  func(ctx context.Context, id string) error
    Written func() []editrig.Change          // what the last Save reported through OnWrite
    SetRaw  func(ctx context.Context, id, field string, value any) error // write bypassing Save
}
```

`Run` dictates a six-field fixture the implementation maps onto its own storage: `id` (read-only), `title` (required on create), `note` (nullable), `counter` (nullable number), `touched` (nullable RFC 3339 timestamp) and `ro` (read-only). Field names are exported constants (`storetest.FieldTitle`, …) so a typo is a compile error. `SetRaw` exists for exactly one check, and it is the important one: a read-only field cannot be put there through `Save`, yet proving that `Save` does not *erase* it is the only way to tell "read-only" apart from "wrote nil".

The optional sub-suites are called in addition to `Run`, with the same factory, and only by an implementation that supports them — a document store has no child tables at all:

| suite | what it adds |
|---|---|
| `RunSatellite` | a field living outside the main row, arriving as a `key → string` map; "no key ⇒ do not touch" one level deeper, and a satellite-only write is still a real write in one transaction |
| `RunExtraTypes` | integer, wall-clock timestamp and array columns: an integer comes back an integer, a wall-clock value grows no timezone, and for an array `[]` and NULL stay different states with order and duplicates preserved |
| `RunChildRows` | repeatable blocks as a child table, scalar (`values`) and object (`items`) elements: an item's own identity survives a save while the list is rebuilt around it |
| `RunKeyedChildRows` | the fourth cell of that matrix — a partitioned child table of objects, where "no key ⇒ do not touch" applies per partition |

`adapter/erjet` passes all five.

## Dependencies

The root package imports only the standard library and `validate`. `validate` imports `santhosh-tekuri/jsonschema/v6` (structural validation) and `golang.org/x/text` (error message printing). `decl` imports only `ui`; `ui` imports nothing. Routers and drivers are leaves of the tree:

- `router/erchi`: [go-chi/chi](https://github.com/go-chi/chi) v5
- `router/erstd`: none
- `adapter/erjet`: go-jet, pgx

The dependency is one-way by construction: an adapter or router imports the core, never the reverse, so a program using `erstd` and its own store compiles without chi or pgx.

## Links

- Client: [`@qrotux/editrig-shadcn-react`](https://github.com/qrotux/editrig-shadcn-react)
- API reference: [pkg.go.dev/github.com/qrotux/editrig-go](https://pkg.go.dev/github.com/qrotux/editrig-go)

## License

MIT
