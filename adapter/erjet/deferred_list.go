package erjet

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/qrotux/editrig-go/ui"
)

// DeferredList is a list whose elements materialize in the second phase: each
// runs through resolve on the same transaction, and only the result reaches
// the wrapped cell. The list counterpart of Deferred (the value exists only
// once the parent row does).
//
// A wrapper outside rather than a Deferred cell inside ChildValues: that one
// panics on a cell without value, and Deferred has none by construction -
// there is nothing to fill INSERT ... VALUES with. Hence the order: first the
// whole list becomes values the wrapped cell can insert, then it runs.
func DeferredList(c Column, resolve DeferredFunc) Column {
	if c.save == nil {
		panic("erjet: DeferredList requires a cell that writes in a second phase (ChildValues and friends) — a cell without save could never write the list at all")
	}
	if c.wire.Kind != ui.KindList {
		panic("erjet: DeferredList requires a list cell — the wrapped cell declares wire kind " + string(c.wire.Kind) + ", and a non-list value would reach resolve as a whole")
	}
	inner := c.save
	out := c
	out.save = func(ctx context.Context, tx pgx.Tx, parentID string, v any) (any, error) {
		list, ok := v.([]any)
		if !ok {
			// Not a list (including the nil of "key absent from the
			// payload"): the wrapped cell decides what to do with it.
			return inner(ctx, tx, parentID, v)
		}
		next := make([]any, 0, len(list))
		for _, item := range list {
			resolved, err := resolve(ctx, tx, parentID, item)
			if err != nil {
				return nil, err
			}
			// nil means the element did not parse: skip it, as replaceValues
			// skips an unparsed row - the positions of the rest stay
			// contiguous.
			if resolved == nil {
				continue
			}
			next = append(next, resolved)
		}
		return inner(ctx, tx, parentID, next)
	}
	return out
}
