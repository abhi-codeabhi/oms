package pg

import (
	"errors"

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
