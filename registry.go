package editrig

import (
	"errors"
	"fmt"
	"sort"
)

// NewRegistry builds a registry from entities, validating each one and
// rejecting duplicate names.
func NewRegistry(entities ...Entity) (*Registry, error) {
	r := &Registry{entities: map[string]*Entity{}}
	for i := range entities {
		e := entities[i]
		if err := validateEntity(&e); err != nil {
			return nil, fmt.Errorf("editor entity %q: %w", e.Name, err)
		}
		if _, dup := r.entities[e.Name]; dup {
			return nil, fmt.Errorf("duplicate editor entity %q", e.Name)
		}
		r.entities[e.Name] = &e
	}
	return r, nil
}

// Get returns the Entity registered under name.
func (r *Registry) Get(name string) (*Entity, bool) { e, ok := r.entities[name]; return e, ok }

// Names returns the entity names of the registry in sorted order.
//
// Entities live in a map and Go randomizes map iteration, so a test walking
// the registry would otherwise report in a flickering order. Registration
// order is deliberately NOT preserved: it is a property of the NewRegistry
// call, not of the registry, and sorting gives one order for every way of
// assembling the same set.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.entities))
	for name := range r.entities {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func validateEntity(e *Entity) error {
	if e.Name == "" {
		return errors.New("empty name")
	}
	if e.Schema == nil || e.Load == nil {
		return errors.New("Schema/Load must be non-nil")
	}
	return nil
}
