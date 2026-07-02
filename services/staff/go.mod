module github.com/restorna/platform/services/staff

go 1.22

require (
	connectrpc.com/connect v1.16.2
	github.com/jackc/pgx/v5 v5.6.0
	github.com/restorna/platform/gen/go v0.0.0
	github.com/restorna/platform/pkg v0.0.0
	github.com/rs/zerolog v1.33.0
	golang.org/x/net v0.27.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.1 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	golang.org/x/sync v0.7.0 // indirect
	golang.org/x/sys v0.22.0 // indirect
	golang.org/x/text v0.16.0 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
)

// Sibling modules are tied together by the root go.work; these replace
// directives let `go build` resolve them when the workspace is not active.
replace github.com/restorna/platform/pkg => ../../pkg

replace github.com/restorna/platform/gen/go => ../../gen/go
