package pg

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	pkgerrors "github.com/restorna/platform/pkg/errors"
	"github.com/restorna/platform/services/billing/internal/domain"
)

// mapRead normalises read errors: no rows becomes the domain ErrNotFound sentinel.
func mapRead(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

// mapWrite normalises write errors: a unique-constraint collision becomes the
// shared ErrAlreadyExists sentinel; everything else passes through.
func mapWrite(err error) error {
	if err == nil {
		return nil
	}
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: duplicate", pkgerrors.ErrAlreadyExists)
	}
	return err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
