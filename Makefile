# Restorna Platform — developer workflow
SERVICES := identity tenant entitlements settings staff onboarding gateway \
            catalog ordering kitchen floor billing promotions servicerequests \
            connectorhub payments notifications aggregators

.PHONY: tools gen lint test up down migrate build
tools: ## install codegen + lint tooling
	go install github.com/bufbuild/buf/cmd/buf@latest
	go install github.com/pressly/goose/v3/cmd/goose@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest

gen: ## generate Go from proto (run after any proto change)
	buf lint
	buf generate

lint:
	golangci-lint run ./...

test: ## unit tests across the workspace
	go test ./...

up: ## start local infra (postgres, nats, redis, jaeger)
	docker compose -f deploy/docker-compose.yml up -d

down:
	docker compose -f deploy/docker-compose.yml down

migrate: ## run migrations for every service (expects DATABASE_URL_<svc> or per-svc dsn)
	@for s in $(SERVICES); do \
		echo "migrating $$s"; \
		goose -dir services/$$s/migrations postgres "$$DATABASE_URL" up || true; \
	done

build: ## build all service binaries
	@for s in $(SERVICES); do (cd services/$$s && go build ./...); done

run/%: ## run a single service, e.g. make run/identity
	cd services/$* && go run ./cmd/server
