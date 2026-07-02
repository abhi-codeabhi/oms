# syntax=docker/dockerfile:1
# Multi-stage build for the identity service.
# Build context MUST be the repo root (the service depends on sibling modules
# ./pkg and ./gen/go via the go.work workspace):
#     docker build -f services/identity/Dockerfile -t restorna/identity .
#
# Runs on Render / ECS / K8s: the process binds to $PORT (default 8080).

# ---- build stage ----
FROM golang:1.22-alpine AS build
WORKDIR /src

# Copy the sibling modules this service depends on via replace, then the service.
# NOTE: do NOT copy go.work — it lists all 18 modules and would force workspace
# mode while only this service's source is in context. Building in module mode
# from the service dir uses the go.mod replace directives (../../pkg, ../../gen/go).
COPY pkg/ ./pkg/
COPY gen/ ./gen/
COPY services/identity/ ./services/identity/

# Build a static binary from the service module (module mode, not workspace).
WORKDIR /src/services/identity
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/identity ./cmd/server

# ---- runtime stage ----
# Distroless static + nonroot: minimal attack surface, no shell, UID 65532.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/identity /identity

# Documented default; the platform injects $PORT and the server honors it.
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/identity"]
