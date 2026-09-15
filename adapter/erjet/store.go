package erjet

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/qrotux/editrig-go"
)

// DB is the subset of a pool Store needs: read a row and open a transaction.
// Declared here rather than taken from pgxpool for the same reason as Querier:
// the package pulls in neither the driver pool nor the engine. *pgxpool.Pool
// satisfies it structurally.
type DB interface {
	Querier
	Begin(context.Context) (pgx.Tx, error)
}

// Store is Load/Save/Delete of one table, derived from its declaration; the
// entity keeps only what is genuinely its own: the insert (which columns are
// filled at creation) and business validation.
//
// The store knows no audit journal: it only reports what it wrote (OnWrite),
// and whoever installed the hook writes the journal. It reports in the core
// type editrig.Change rather than its own so the application writes one
// bridge regardless of which store it uses.
type Store struct {
	// DB is the source of reads and transactions (a pool or a wrapper of one).
	DB DB
	// Table is the declared table the SELECT and partial UPDATE are built from.
	Table Table
	// Touch is the "last touched" column, set to the server's now() on every
	// UPDATE. nil means it is not written.
	Touch postgres.ColumnTimestampz
	// Create performs the insert. Save owns the transaction and the commit, so
	// the hook must neither commit nor report the write itself: the journal
	// could otherwise get ahead of a failed commit. Which columns are filled at
	// creation is the entity's decision, which is why the hook builds the query.
	Create func(ctx context.Context, tx pgx.Tx, in map[string]any) (outID string, changes []editrig.Change, err error)
	// OnWrite reports what was written. Called after a successful commit and
	// only when something actually changed. created distinguishes an insert
	// from an update: the id of an insert only exists now, and the journal
	// needs it separately. nil means nobody listens.
	OnWrite func(ctx context.Context, id string, changes []editrig.Change, created bool)
}

// Load reads every declared column of the row; (nil, nil) means no row.
func (s Store) Load(ctx context.Context, id string) (map[string]any, error) {
	return s.Table.Row(ctx, s.DB, s.Table.Order(), id)
}

// Save creates (id == nil) or partially updates (id != nil) a row.
//
// The UPDATE is the intersection of the payload with the writable columns of
// the declaration: a key absent from the payload yields no assignment, so the
// column is left untouched rather than rewritten "with the same value". An
// explicit JSON null stays an explicit SQL NULL.
//
// The signature is exactly the core's Entity.Save, so the method plugs into
// Entity without an adapter. ferr is always nil: business validation lives in
// Entity.Validate and runs earlier. "No row" is editrig.ErrNotFound, which the
// core turns into a 404.
//
// The store opens the transaction because the engine knows no driver and
// cannot roll back on its behalf. The deferred Rollback covers every early
// return and is a no-op after Commit.
func (s Store) Save(ctx context.Context, id *string, in map[string]any) (outID string, ferr []editrig.FieldError, err error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Deferred cells are both satellites (value in another table) and deferred
	// columns (value depends on an already existing row, see Column.save and
	// Deferred). Computed once: the list depends only on the payload, not on
	// whether this is a create or an update.
	deferred := s.Table.Deferred(in)

	// assignsBack holds the assignments that go out in the second UPDATE. A
	// separate accumulator, not assigns: on update the first phase has already
	// executed its SET, and appending the deferred phase to it would repeat the
	// whole SET, Touch = now() included, on every save.
	var assignsBack []postgres.ColumnAssigment
	var rowID string
	var changed []string
	var newVals map[string]any
	var before map[string]any // update only: on create there is no "before" to read

	if id == nil {
		newID, hookChanges, err := s.Create(ctx, tx, in)
		if err != nil {
			return "", nil, err
		}
		rowID = newID
		// On create the ordinary columns are written by a second statement, not
		// by the INSERT: the hook inserts exactly what the row cannot exist
		// without (the entity's decision), and the rest of the payload becomes
		// ordinary assignments. Without this a full create form would silently
		// drop every column the hook does not know.
		assignsBack, changed, newVals = s.Table.Assignments(in)
		changed, newVals = mergeHookChanges(hookChanges, changed, newVals)
	} else {
		rowID = *id
		assigns, ch, nv := s.Table.Assignments(in)
		changed, newVals = ch, nv

		// No writable column arrived (e.g. the payload consisted of read-only
		// fields only) and no deferred cell either. Nothing to write, but "no
		// row" still has to be told apart from "nothing to write", and an
		// existence check is enough for that.
		if len(assigns) == 0 && len(deferred) == 0 {
			ok, err := s.Table.Exists(ctx, tx, rowID)
			if err != nil {
				return "", nil, err
			}
			if !ok {
				return "", nil, editrig.ErrNotFound
			}
			return rowID, nil, nil
		}

		// Old values of exactly the fields about to be written (columns and
		// deferred together): the diff needs them (Change carries old and new)
		// and they prove the row exists. This read serves the journal, not a
		// merge: it substitutes nothing into the write. Row reads satellites
		// itself, so only the lists are joined here.
		before, err = s.Table.Row(ctx, tx, append(append([]string{}, changed...), names(deferred)...), rowID)
		if err != nil {
			return "", nil, err
		}
		if before == nil {
			return "", nil, editrig.ErrNotFound
		}

		// The branch is mandatory: a payload of deferred fields only yields no
		// assignment, and UpdateSQL panics on an empty SET.
		if len(assigns) > 0 {
			sql, args := s.Table.UpdateSQL(assigns, s.Touch, rowID)
			if _, err := tx.Exec(ctx, sql, args...); err != nil {
				return "", nil, err
			}
		}
	}

	// The deferred phase runs after the main table and before the commit, in
	// the same transaction: that is why it receives tx and not the pool. rowID
	// is never empty here (the freshly created id, or the id from the URL), and
	// an early return takes the INSERT and the first UPDATE down with it.
	//
	// KeyedStrings cells sharing one side table are written by a single upsert
	// up front rather than each by its own save: see KeyedTable.writeMany for
	// why a one-column write breaks on a NOT NULL neighbour. handled marks the
	// names already written by that pass; for them the loop reads the result
	// back instead of writing again through save.
	handled, err := writeKeyedGroups(ctx, tx, rowID, deferred, in)
	if err != nil {
		return "", nil, err
	}
	for _, nc := range deferred {
		var v any
		var err error
		if handled[nc.Name] {
			sc := nc.Column.satellite
			v, err = sc.table.read(ctx, tx, sc.col, rowID)
		} else {
			v, err = nc.Column.save(ctx, tx, rowID, in[nc.Name])
		}
		if err != nil {
			return "", nil, err
		}
		// nil means the cell did not parse the value and wrote nothing, the
		// same decision as ok == false in assign. Such a field stays out of the
		// diff, otherwise the journal would report a change that did not
		// happen.
		if v == nil {
			continue
		}
		changed = append(changed, nc.Name)
		newVals[nc.Name] = v
		// A satellite has no column of its own to assign; only a deferred
		// column travels in the second UPDATE.
		if !nc.Column.IsSatellite() {
			assignsBack = append(assignsBack, nc.Column.assignOf(v))
		}
	}
	// The branch is mandatory: UpdateSQL panics on an empty SET, and a submit
	// without a single column change is legal input.
	if len(assignsBack) > 0 {
		sql, args := s.Table.UpdateSQL(assignsBack, s.Touch, rowID)
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return "", nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", nil, err
	}

	if id == nil {
		// Built by its own loop rather than diff(keys, before, after): on
		// create there is no "before" to read, the row did not exist.
		changes := make([]editrig.Change, 0, len(changed))
		for _, k := range changed {
			changes = append(changes, editrig.Change{Field: k, New: newVals[k]})
		}
		s.wrote(ctx, rowID, changes, true)
		return rowID, nil, nil
	}

	s.wrote(ctx, rowID, diff(changed, before, newVals), false)
	return rowID, nil, nil
}

// mergeHookChanges folds the Create hook's diff into the assignments diff. An
// overlap is normal: a column the hook inserted also arrives through
// Assignments when it is in the payload. Assignments wins; the value is the
// same, and a field must have one source or the journal reports it twice.
func mergeHookChanges(hook []editrig.Change, changed []string, newVals map[string]any) ([]string, map[string]any) {
	for _, c := range hook {
		if _, ok := newVals[c.Field]; ok {
			continue // already reported by Assignments
		}
		changed = append(changed, c.Field)
		newVals[c.Field] = c.New
	}
	return changed, newVals
}

// Delete removes the row by id; existence is not checked because the engine
// already did so through Load before calling.
func (s Store) Delete(ctx context.Context, id string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	sql, args := s.Table.DeleteSQL(id)
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		return conflictErr(err)
	}
	return tx.Commit(ctx)
}

// ErrConstraintConflict reports a delete rejected by a database constraint:
// the row is still referenced (23503) or nulling the reference is forbidden by
// NOT NULL (23502).
//
// A sentinel rather than a ready editrig.ConflictError: the catalog key of the
// message is the application's knowledge, and the store knows no catalog.
var ErrConstraintConflict = errors.New("erjet: constraint conflict")

// conflictErr maps the driver's constraint codes to the sentinel, keeping the
// original error in the chain; anything else passes through.
func conflictErr(err error) error {
	if err == nil {
		return nil
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "23503" || pg.Code == "23502") {
		return fmt.Errorf("%w (%s): %w", ErrConstraintConflict, pg.Code, err)
	}
	return err
}

// wrote reports a committed write to the listener, if any.
//
// An empty diff is reported too: on insert the hook also carries the new row's
// id, and staying silent would take the journal's target away. What to do with
// an empty diff is the listener's decision.
func (s Store) wrote(ctx context.Context, id string, changes []editrig.Change, created bool) {
	if s.OnWrite == nil {
		return
	}
	s.OnWrite(ctx, id, changes, created)
}

// names lists the field names of a set of cells.
func names(cols []Named) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name
	}
	return out
}

// diff builds the change rows for the fields actually written. Both sides are
// already in wire shape (before from the read, after from Assignments), so
// comparing string representations yields no false changes on timestamps and
// numbers.
func diff(keys []string, before, after map[string]any) []editrig.Change {
	var out []editrig.Change
	for _, k := range keys {
		oldV, newV := before[k], after[k]
		if fmt.Sprint(oldV) == fmt.Sprint(newV) {
			continue
		}
		out = append(out, editrig.Change{Field: k, Old: oldV, New: newV})
	}
	return out
}
