# syntax=docker/dockerfile:1
# Multi-stage, distroless, non-root. Build CONTEXT is the repo root so the sibling
# modules referenced by go.mod replaces (../../gen/go, ../../pkg) are available:
#   docker build -f services/servicerequests/Dockerfile -t restorna/servicerequests .

FROM golang:1.22-alpine AS build
WORKDIR /src

# Copy the modules this service depends on (replace targets) + the service itself.
COPY pkg/ ./pkg/
COPY gen/ ./gen/
COPY services/servicerequests/ ./services/servicerequests/

WORKDIR /src/services/servicerequests
RUN go mod download
# Static, stripped binary.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# Distroless runtime, non-root (uid 65532).
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /app/server

# Platform (Render/ECS/K8s) injects PORT; the app reads it via pkg/config.
# SETTINGS_URL selects the SettingsService endpoint (cooldown/escalation thresholds).
ENV PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/server"]
