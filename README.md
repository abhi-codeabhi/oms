# Restorna Platform

Multi-tenant restaurant-OS **SaaS** — control plane (onboarding, brands, outlets,
plans, staff limits, branding) + OMS data plane + plug-and-play integrations
(payments, aggregators, CRM/ERP), built as **Go microservices** in a **monorepo**,
deployable to **any Kubernetes**.

- Architecture: [`ARCHITECTURE.md`](./ARCHITECTURE.md)
- Engineering rules: [`CONVENTIONS.md`](./CONVENTIONS.md)

## Layout
```
proto/      protobuf contracts (Buf module) — the source of truth between services
gen/go/     generated Go (do not edit)
pkg/        shared libraries (auth, tenancy, money, ids, outbox, events, connector SDK…)
services/   one Go module per bounded context (hexagonal)
deploy/     docker-compose (local), Helm charts, Kustomize overlays
web/        platform console + reused staff/owner React apps
```

## Quick start (requires Go 1.22+, Buf, Docker)
```bash
make tools        # install buf, goose, golangci-lint, protoc plugins
make gen          # generate Go from proto
make up           # start local infra (Postgres, NATS, Redis, Jaeger) via docker-compose
make migrate      # run all service migrations
make test         # go test ./... across the workspace
make run/identity # run a single service locally
```

## Milestones
- **M1 (now): control plane** — identity, tenant, entitlements, staff, onboarding,
  gateway + platform/owner consoles.
- M2: OMS data plane (catalog, ordering, kitchen, floor, billing, promotions, requests).
- M3: integration plane (connector-hub, payments, aggregators, notifications).
- M4: analytics, CRM/ERP, scale & hardening.

> Note: this repo is contract-first. Add or change a service's API in `proto/` and
> run `make gen` before implementing. Never edit `gen/`.
