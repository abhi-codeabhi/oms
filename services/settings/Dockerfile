# syntax=docker/dockerfile:1

# ---- build stage ----------------------------------------------------------
# Build from the monorepo root so the sibling gen/go + pkg modules (referenced
# by replace directives) are in context. Build context = repo root:
#   docker build -f services/settings/Dockerfile -t restorna/settings .
FROM golang:1.22-alpine AS build
WORKDIR /src

# Copy the modules this service depends on via replace, then the service itself.
# NOTE: do NOT copy go.work here — it lists all 18 modules and would force
# workspace mode, but only this service's source is in context. Building in
# module mode uses the go.mod replace directives (../../pkg, ../../gen/go).
COPY pkg/ ./pkg/
COPY gen/ ./gen/
COPY services/settings/ ./services/settings/

WORKDIR /src/services/settings
RUN go mod download
# Static, stripped binary; no cgo for distroless.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/server ./cmd/server

# ---- runtime stage --------------------------------------------------------
# Distroless static, non-root (uid 65532 "nonroot"). Reads PORT from env.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/server /server

ENV PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]
