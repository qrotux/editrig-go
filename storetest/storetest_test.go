package storetest

import (
	"context"
	"fmt"
	"testing"

	"github.com/qrotux/editrig-go"
)

// memStore is the in-memory reference implementation of the suite. It proves
// the suite is satisfiable without Postgres specifics, and it reads as
// executable documentation of what a new store has to implement.
type memStore struct {
	rows map[string]map[string]any
	next int
	last []editrig.Change
}

// writable lists the fixture fields that accept a write, the analogue of "the
// cell has an assign": everything else is physically unreachable for writes.
var writable = map[string]bool{FieldTitle: true, FieldNote: true, FieldCounter: true, FieldTouched: true}

func newMemStore() *memStore { return &memStore{rows: map[string]map[string]any{}} }

func (m *memStore) Load(_ context.Context, id string) (map[string]any, error) {
	row, ok := m.rows[id]
	if !ok {
		return nil, nil // "no row" is not an error
	}
	out := make(map[string]any, len(row))
	for k, v := range row {
		// The satellite is deep-copied: handing out the same map would let the
		// caller edit the store behind Save's back, a link a real store lacks.
		if sub, isMap := v.(map[string]any); isMap {
			out[k] = copyMap(sub)
			continue
		}
		out[k] = v
	}
	return out, nil
}

// mergeLocalized writes the satellite by merging per key rather than replacing
// the map: an absent key stays as it was, an empty string (or null) erases its
// key. This is the "partial one level deeper" rule the sub-suite exists for.
func mergeLocalized(row map[string]any, want any) (editrig.Change, bool) {
	patch, ok := want.(map[string]any)
	if !ok {
		return editrig.Change{}, false
	}
	before, _ := row[FieldLocalized].(map[string]any)
	next := copyMap(before)
	for k, v := range patch {
		s, _ := v.(string)
		if v == nil || s == "" {
			delete(next, k)
			continue
		}
		next[k] = s
	}
	row[FieldLocalized] = next
	// fmt prints maps with sorted keys, so "the same in another order" does
	// not become a false change.
	if fmt.Sprint(before) == fmt.Sprint(next) {
		return editrig.Change{}, false
	}
	return editrig.Change{Field: FieldLocalized, Old: before, New: next}, true
}

func copyMap(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func (m *memStore) Save(_ context.Context, id *string, in map[string]any) (string, []editrig.FieldError, error) {
	if id == nil {
		m.next++
		newID := fmt.Sprintf("id-%d", m.next)
		title := in[FieldTitle]
		row := map[string]any{FieldID: newID, FieldTitle: title}
		m.rows[newID] = row
		m.last = []editrig.Change{{Field: FieldTitle, New: title}}
		// The rest of the payload is written the same way as on update: a
		// minimal insert is the store's decision, not a reason to drop the
		// satellites and other columns that arrived together with the title.
		rest := make(map[string]any, len(in))
		for field, want := range in {
			if field != FieldTitle {
				rest[field] = want
			}
		}
		m.applyWritable(row, rest)
		return newID, nil, nil
	}

	row, ok := m.rows[*id]
	if !ok {
		return "", nil, editrig.ErrNotFound
	}
	m.last = nil
	m.applyWritable(row, in)
	return *id, nil, nil
}

// applyWritable diffs and writes in one pass: only the writable keys present in
// the payload are written, only those that actually changed are reported.
// Shared by create (after the minimal insert) and update.
func (m *memStore) applyWritable(row map[string]any, in map[string]any) {
	for field, want := range in {
		// The satellite is written by a per-key merge (mergeLocalized), and its
		// writability is the existence of that merge, not an entry in writable.
		if field == FieldLocalized {
			if ch, changed := mergeLocalized(row, want); changed {
				m.last = append(m.last, ch)
			}
			continue
		}
		if !writable[field] {
			continue
		}
		before := row[field]
		if fmt.Sprint(before) == fmt.Sprint(want) {
			continue
		}
		row[field] = want
		m.last = append(m.last, editrig.Change{Field: field, Old: before, New: want})
	}
}

func (m *memStore) Delete(_ context.Context, id string) error {
	delete(m.rows, id) // deleting twice is not an error
	return nil
}

func (m *memStore) setRaw(_ context.Context, id, field string, value any) error {
	row, ok := m.rows[id]
	if !ok {
		return editrig.ErrNotFound
	}
	row[field] = value
	return nil
}

// TestReferenceStore runs the suite against the reference implementation.
func TestReferenceStore(t *testing.T) {
	Run(t, newReference)
}

// TestReferenceStoreSatellite pins that the optional sub-suite is satisfiable
// by the same small in-memory store and needs no Postgres specifics.
func TestReferenceStoreSatellite(t *testing.T) {
	RunSatellite(t, newReference)
}

func newReference(t *testing.T) Subject {
	t.Helper()
	m := newMemStore()
	return Subject{
		Load:    m.Load,
		Save:    m.Save,
		Delete:  m.Delete,
		Written: func() []editrig.Change { return m.last },
		SetRaw:  m.setRaw,
	}
}
