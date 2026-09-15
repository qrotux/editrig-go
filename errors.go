package editrig

import "errors"

// ErrNotFound reports that no entity has the given id; returned by Entity.Save
// implementations, the engine answers 404 instead of 500.
//
// A sentinel rather than a FieldError shape: a string inside a field error
// (FieldError{Message: "not found"}) would be a CONVENTION known only to an
// adapter inside the application. A storage implementation living apart from
// it (see adapter/) cannot guess such a convention, and a typo in it is caught
// by nothing: a 404 would silently turn into a 422 with a puzzling text.
//
// The regular update path catches a missing row with the preliminary Load; what
// lands here is a race, the row deleted BETWEEN Load and Save. Both paths give
// 404 instead of a validation error out of nowhere.
//
// Entity.Load reports the same fact differently, as (nil, nil), and that is
// deliberate: there "no row" is expressed by the absence of data and requires
// no core symbol from the store. The sentinel is needed where there is exactly
// one channel.
var ErrNotFound = errors.New("editor: entity not found")

// ConflictRef is one source of references in a refused delete: the catalog key
// of its label and the number of references.
type ConflictRef struct {
	LabelKey string
	Count    int
}

// ConflictError reports a mutation refused by the STATE of the data, not by its
// shape.
//
// It carries catalog KEYS and numbers, not a rendered string: Entity.Delete
// (func(ctx, id) error) has no access to a Catalog, so the entity has nothing
// to localize the message with. The handler renders it, having already built
// the catalog (handler.catalog). Hence the form, "key + count" pairs instead of
// formatted text: formatting is locale-neutral, words are not.
type ConflictError struct {
	Key  string
	Refs []ConflictRef
}

func (e *ConflictError) Error() string { return "editor: conflict: " + e.Key }
