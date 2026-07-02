module github.com/restorna/platform/services/identity

go 1.22

require (
	connectrpc.com/connect v1.16.2
	github.com/jackc/pgx/v5 v5.6.0
	github.com/restorna/platform/gen/go v0.0.0
	github.com/restorna/platform/pkg v0.0.0
)

require (
	github.com/golang-jwt/jwt/v5 v5.2.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.1 // indirect
	github.com/oklog/ulid/v2 v2.1.0 // indirect
	github.com/pressly/goose/v3 v3.21.1 // indirect
	github.com/rs/zerolog v1.33.0 // indirect
	golang.org/x/crypto v0.24.0 // indirect
	golang.org/x/net v0.27.0 // indirect
	golang.org/x/sync v0.7.0 // indirect
	golang.org/x/text v0.16.0 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
)

// The shared pkg + generated proto are sibling modules tied via the root
// go.work; these replaces let `go build` resolve them without a proxy.
replace (
	github.com/restorna/platform/gen/go => ../../gen/go
	github.com/restorna/platform/pkg => ../../pkg
)
