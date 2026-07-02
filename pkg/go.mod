module github.com/restorna/platform/pkg

go 1.22

require (
	connectrpc.com/connect v1.16.2
	github.com/golang-jwt/jwt/v5 v5.2.1
	github.com/jackc/pgx/v5 v5.6.0
	github.com/nats-io/nats.go v1.36.0
	github.com/oklog/ulid/v2 v2.1.0
	github.com/pressly/goose/v3 v3.21.1
	github.com/restorna/platform/gen/go v0.0.0
	github.com/rs/zerolog v1.33.0
	go.opentelemetry.io/otel v1.28.0
	go.opentelemetry.io/otel/trace v1.28.0
	golang.org/x/net v0.27.0
)

// gen/go is generated locally by `make gen`; resolve it from the monorepo tree
// rather than the network. go.work also wires this for multi-module builds.
replace github.com/restorna/platform/gen/go => ../gen/go

