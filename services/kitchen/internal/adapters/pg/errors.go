package pg

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	pkgerrors "github.com/restorna/platform/pkg/errors"
)

// mapWrite normalises write errors: a unique-constraint collision becomes the
// shared ErrAlreadyExists sentinel (the grpc adapter would map it to
// CodeAlreadyExists); everything else passes through.
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
