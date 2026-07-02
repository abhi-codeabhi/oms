# Heroku / Railway / Dokku style process declarations.
# Each process is one service binary; the platform injects $PORT and the binary
# binds it (12-factor). These names map 1:1 to services/<name>. Build with the
# Go buildpack or the per-service Dockerfile (preferred — keeps the single image).
identity: ./bin/identity
tenant: ./bin/tenant
entitlements: ./bin/entitlements
staff: ./bin/staff
