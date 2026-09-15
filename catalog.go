package editrig

import "net/http"

// Key convention of the editor i18n catalog. It lives in the CORE because it
// is a shape three places must agree on: the message catalog, the coverage
// gate and the entity itself.
//
// The engine substitutes the entity name into the key (Copy receives it from
// Entity.Name); the entity never sees keys at all. Were it to write the prefix
// itself ("editor.users."+name), the word "users" would stand there and in
// Entity.Name independently, and a drift would give not an error but a silent
// fallback: the application's translator returns the key itself, and the form
// would show the title "editor.usres.city".
//
// The LABELS are not resolved here: how a key becomes a string is the
// application's business (see Catalog).

// catalogRoot is the root namespace of the framework; it matches the namespace
// prefix of the application's message catalog.
const catalogRoot = "editor."

// Key builds the catalog key editor.<entity>.<suffix>, the base form the other
// keys derive from.
func Key(entity, suffix string) string { return catalogRoot + entity + "." + suffix }

// ValueKey builds the label key of an enum VALUE:
// editor.<entity>.<field>_values.<value>.
//
// The "_values" suffix instead of nesting under the field name is forced: in a
// nested-JSON catalog the key editor.users.role cannot be both a string (the
// field title) and an object (the value labels).
func ValueKey(entity, field, value string) string { return Key(entity, field+"_values."+value) }

// FieldKey builds the title key of a SUBFIELD of a repeatable block's item:
// editor.<entity>.<field>_fields.<sub>.
//
// The "_fields" suffix exists for the same reason as "_values" above, with the
// same consequences: the catalog is nested JSON, and editor.<entity>.<field>
// cannot be both a string (the list title) and an object (the subfield
// titles). Mirrored by the constant ui.SubfieldSuffix, since the vocabulary and
// the engine do not import each other.
func FieldKey(entity, field, sub string) string { return Key(entity, field+"_fields."+sub) }

// GroupKey builds the title key of a form section: editor.<entity>.groups.<id>.
func GroupKey(entity, id string) string { return Key(entity, "groups."+id) }

// Catalog is the source of the LOCALIZED COPY of one entity for one request.
//
// An INTERFACE, not a struct: HOW the copy is resolved (by session locale,
// tenant, brand) only the application knows, and that knowledge never reaches
// the engine (no locale and no translator in its signatures). The engine only
// asks for a label by field name, value or section id.
//
// The entity receives ALREADY LOCALIZED labels and touches neither keys nor
// prefixes nor its own name, so there is still nowhere to mix them up: the
// engine substitutes the entity name when calling Copy.
//
// Title/Label fit the signature of decl.Set.Fields and are passed there as
// method values, without an intermediate closure.
type Catalog interface {
	Title(field string) string
	Label(field, value string) string
	Group(id string) string
	Message(suffix string) string
}

// Copy is the per-request catalog factory.
//
// entity comes FROM THE ENGINE (Entity.Name), not from the application: the
// guarantee "the entity name in keys and in the registry is one literal" holds
// even though the application builds the catalog.
//
// *http.Request is here because the axis of the copy is a property of the
// REQUEST (session, header, host), not of the process. What exactly to read
// from the request the engine neither knows nor decides.
type Copy func(r *http.Request, entity string) Catalog

// PlainCatalog uses field names and values as labels without localization.
type PlainCatalog struct{}

func (PlainCatalog) Title(field string) string        { return field }
func (PlainCatalog) Label(field, value string) string { return value }
func (PlainCatalog) Group(id string) string           { return id }
func (PlainCatalog) Message(suffix string) string     { return suffix }
