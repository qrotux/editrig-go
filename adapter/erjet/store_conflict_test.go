package erjet

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestConflictErrClassifiesConstraintCodes(t *testing.T) {
	for _, code := range []string{"23503", "23502"} {
		err := conflictErr(&pgconn.PgError{Code: code, Message: "violation"})
		if !errors.Is(err, ErrConstraintConflict) {
			t.Errorf("code %s: errors.Is(ErrConstraintConflict) = false, want true", code)
		}
	}
}

func TestConflictErrPassesOtherErrorsThrough(t *testing.T) {
	other := &pgconn.PgError{Code: "23505", Message: "unique"}
	if err := conflictErr(other); !errors.Is(err, other) {
		t.Errorf("unique violation must pass through unchanged, got %v", err)
	}
	plain := errors.New("boom")
	if err := conflictErr(plain); !errors.Is(err, plain) {
		t.Errorf("plain error must pass through unchanged, got %v", err)
	}
	if conflictErr(nil) != nil {
		t.Error("nil must stay nil")
	}
}
