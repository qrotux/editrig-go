// Package editrig is a portable full-CRUD engine for admin editors: a JSON
// Schema descriptor (Entity.Schema), structural validation (package validate)
// and a Handler with six operations (schema/load/create/update/delete/options)
// over the Entity hooks (Load/Save/Delete/Validate).
//
// DEPENDENCY CONTRACT: only the standard library and this module's validate
// package, which in turn imports only github.com/santhosh-tekuri/jsonschema/v6
// (+/kind) and golang.org/x/text/{message,language}. No router, no database
// driver of any kind; `go list -deps .` is how the contract is checked.
//
// THE ENGINE KNOWS NO LOCALE either: there is no locale and no translator in
// any signature. It asks a Catalog for labels, an INTERFACE the application
// implements (see catalog.go), and builds one per request through the Copy
// factory, substituting only the entity name. The axis of the copy (session
// locale, tenant, brand) is the application's property.
//
// THE ENGINE KNOWS NO STORAGE: no driver type in any signature, atomicity is the
// entity's concern (see Entity.Save), and the engine itself stays a protocol:
// HTTP, schema, validation, read-only strip, registry.
//
// THE ENGINE KNOWS NO ROUTER: Handler's methods take their route parameters
// explicitly (Load(w, r, name, id)) and read nothing from the URL. Mounting is
// router/erchi's or router/erstd's job, or six lines on any mux; route
// topology and guard wrapping stay the caller's. Configuration is per Handler
// through HandlerOption values: the unknown-field policy and the request body
// limits.
//
// Only Schema and Load are required of an Entity. A missing Save or Delete,
// or a Disable* flag, turns the corresponding operation into 405; a missing
// Validate is skipped. Structural validation of an update sees the row's
// current state overlaid with the submitted keys, while Validate and Save see
// only what was submitted (validate.Write).
package editrig
