module github.com/restorna/platform/services/gateway

go 1.22

require (
	connectrpc.com/connect v1.16.2
	github.com/restorna/platform/gen/go v0.0.0
	github.com/restorna/platform/pkg v0.0.0
	github.com/rs/zerolog v1.33.0
)

require (
	github.com/golang-jwt/jwt/v5 v5.2.1 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	go.opentelemetry.io/otel v1.28.0 // indirect
	go.opentelemetry.io/otel/trace v1.28.0 // indirect
	golang.org/x/net v0.27.0 // indirect
	golang.org/x/sys v0.22.0 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
)

// Sibling modules are tied together by the root go.work; these replace
// directives let `go build` resolve them when the workspace is not active.
replace github.com/restorna/platform/pkg => ../../pkg

replace github.com/restorna/platform/gen/go => ../../gen/go
