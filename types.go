package editrig

import (
	"context"
	"encoding/json"

	"github.com/qrotux/editrig-go/validate"
)

// FieldError is validate.FieldError: the one type both halves of validation
// (structural in validate, business in Entity.Validate/Save) report in.
type FieldError = validate.FieldError

// Entity is the registration record of one editable type.
type Entity struct {
	Name string

	// DisableCreate, DisableUpdate and DisableDelete restrict operations independently.
	// Missing Save disables create and update; missing Delete disables deletion.
	DisableCreate bool
	DisableUpdate bool
	DisableDelete bool

	// Schema builds the JSON Schema and uiSchema: data==nil is the SHORT schema
	// (create), data!=nil the FULL schema (load/update), which may depend on
	// the current values.
	//
	// cat is the entity's catalog, already bound to the request locale and the
	// entity name (see catalog.go): labels arrive localized, the entity never
	// sees keys.
	Schema func(ctx context.Context, cat Catalog, data map[string]any) (schema, uiSchema json.RawMessage, err error)

	// Load reads the entity by id; (nil, nil) means not found (404).
	Load func(ctx context.Context, id string) (map[string]any, error)

	// Save creates (id==nil) or updates (id!=nil) the entity. ferr!=nil are
	// business validation errors (422), err!=nil an infrastructure error (500),
	// err==ErrNotFound a missing row (404, see errors.go).
	//
	// ATOMICITY IS THE ENTITY'S CONCERN. The engine opens no transaction and
	// knows nothing about one: it has, and must have, no driver type in its
	// signatures, or an entity on another store could not be registered. The
	// entity must roll back its own work when it returns ferr or err.
	//
	// Audit is not part of that atomicity: the application's audit bridge
	// writes AFTER the handler responds, on its own connection.
	Save func(ctx context.Context, id *string, in map[string]any) (outID string, ferr []FieldError, err error)

	// Delete removes the entity by id; atomicity is again the entity's.
	Delete func(ctx context.Context, id string) error

	// Validate is the business validation on top of the structural one (JSON
	// Schema); id==nil on create. cat is the same catalog Schema gets, so
	// domain messages ("username taken") are localized by it too.
	//
	// IT MAY MUTATE THE VALUES IN in. It is the only hook that has both a
	// catalog (localized messages) and a position BEFORE the transaction, so
	// the only place where heavy preparation of a value (decoding an image)
	// can happen outside the row lock and fail with a human-readable error. An
	// entity relying on this must state it in its own declaration.
	Validate func(ctx context.Context, cat Catalog, id *string, in map[string]any) []FieldError

	// Options holds the value sources of relation fields, keyed BY FIELD NAME
	// (that is what arrives in the URL). Empty means the entity has no relation
	// fields and the route answers 404; a field missing from the map is 404
	// too, and the entity needs no separate sentinel for it, the absent key
	// says the same thing.
	//
	// A map rather than one dispatcher function: the source is declared next
	// to its field, and no second list of "which fields are relations" exists,
	// the same principle as decl.Entry, where the cell stands next to the
	// presentation.
	Options map[string]OptionSource
}

// OptionSource is the value source of ONE relation field.
//
// A FUNCTION, not an interface: a closure carries whatever it needs (a Postgres
// pool, a second pool, an in-memory slice, a parsed JSON file, a client of
// another service) and the engine needs to know nothing about it. The engine
// ships no implementation: it has no driver and cannot have one (doc.go).
//
// cat is the same catalog Schema and Validate get, the ONLY request-scope
// channel. Locale, tenant, brand: the application puts into its catalog
// implementation whatever its own sources need.
//
// IMPLEMENTATION CONTRACT (unchecked by the engine, relied on by the widget):
//
//  1. Two modes. q.IDs non-empty means return the labels of EXACTLY those
//     values, ignoring Search and Limit: this is hydration of already selected
//     values. Otherwise search by Search, at most Limit results.
//  2. Limit is already clamped by the engine (default and ceiling in
//     options.go); no need to re-check it.
//  3. Order is significant and stable between calls: the list is read by eye.
//  4. An empty result is an empty slice, not nil.
//  5. Labels are non-empty: with no translation return the id itself, not ""
//     (an empty chip in the form is indistinguishable from a broken one).
//  6. "Nothing found" is an empty slice, not an error; an error means an
//     infrastructure failure and becomes a 500.
//  7. Everything request-scoped comes from cat only. The source has no other
//     channel.
type OptionSource func(ctx context.Context, cat Catalog, q OptionsQuery) ([]Option, error)

// Option is one value of a relation field: the id on the wire and its ready
// LABEL.
//
// The label arrives localized from the backend (Go is the source of truth for
// the domain copy), so the widget needs neither a second request nor knowledge
// of where a collection keeps its name: in the row itself for one, in a side
// table of translations for another.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// OptionsQuery is what an options source is asked.
//
// Search and IDs are two MODES of one hook, not two endpoints: "find by
// substring" and "give me the labels of these ids". The second exists because
// form data holds ids while the form must show names; a separate hook for it
// would duplicate the source declaration on every field.
type OptionsQuery struct {
	Search string
	// Limit is already clamped by the engine (default and ceiling in
	// options.go); the implementation need not re-check it.
	Limit int
	// IDs non-empty means label hydration mode: Search and Limit do not apply.
	IDs []string
	// Parent is the id of the row being edited, for sources whose list depends
	// on the owner (one user's media library); reference lists ignore it.
	// Empty on the create form or when the source does not use it.
	Parent string
}

// Upload is a file that arrived in the form SUBMIT (multipart part
// "file:<field>"). The engine puts it into the payload in place of the field
// value and stops there: what to do with the bytes only the entity knows (it
// holds the category, limit and owner policy). The core can neither decode
// images nor write to storage, and must not.
type Upload struct {
	Filename string
	Mime     string
	Data     []byte
}

// Registry is the set of registered entities by name.
type Registry struct{ entities map[string]*Entity }
