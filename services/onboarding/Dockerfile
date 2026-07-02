# syntax=docker/dockerfile:1

# --- build stage ------------------------------------------------------------
FROM golang:1.22 AS build
WORKDIR /src

# The service module depends on sibling modules (pkg, gen/go) and the root
# module via go.work replace directives. Copy the whole monorepo so the build
# resolves them locally without network module fetches for the workspace.
COPY . .

WORKDIR /src/services/onboarding
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/onboarding ./cmd/server

# --- runtime stage ----------------------------------------------------------
# Distroless, non-root. The server reads PORT (and the rest of its config) from
# the environment per 12-factor; nothing is baked in.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/onboarding /onboarding

# Documented default; the platform sets PORT in the environment.
ENV PORT=8080
EXPOSE 8080

USER nonroot:nonroot
ENTRYPOINT ["/onboarding"]
