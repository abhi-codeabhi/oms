package pg

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	pkgerrors "github.com/restorna/platform/pkg/errors"
)

// errAlreadyExists is the shared sentinel for unique-constraint collisions; the
// grpc adapter maps it to connect.CodeAlreadyExists via pkg/errors.
var errAlreadyExists = pkgerrors.ErrAlreadyExists

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// mapWrite maps a unique-constraint violation to the AlreadyExists sentinel; any
// other error passes through unchanged.
func mapWrite(err error) error {
	if err == nil {
		return nil
	}
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: duplicate", errAlreadyExists)
	}
	return err
}
