package pg

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// isUniqueViolation reports a Postgres 23505 unique-constraint collision, mapped to
// domain.ErrAlreadyExists (then connect.CodeAlreadyExists at the grpc boundary).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
