module github.com/restorna/platform/services/settings

go 1.22

require (
	connectrpc.com/connect v1.16.2
	github.com/jackc/pgx/v5 v5.6.0
	github.com/restorna/platform/gen/go v0.0.0
	github.com/restorna/platform/pkg v0.0.0
)

// In the monorepo these are resolved by the root go.work; the replaces keep the
// module buildable standalone against the sibling source trees.
replace (
	github.com/restorna/platform/gen/go => ../../gen/go
	github.com/restorna/platform/pkg => ../../pkg
)
