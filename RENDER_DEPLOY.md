# Deploying the Restorna Platform to Render

Render can run the whole platform (the `render.yaml` blueprint defines all 18
services + Postgres + private NATS + the web console). It is **not click-to-deploy**
until you do a one-time local build pass, because the Go code is contract-first and
the generated protobuf code must be committed.

## Prerequisites (once, on a machine with the toolchain)
Install **Go 1.22+**, **Buf**, and (optionally) Docker. Then, from the repo root:

```bash
make tools                 # buf, goose, golangci-lint, protoc plugins
make gen                   # buf generate  -> populates gen/go/**  (REQUIRED)
go work sync
# tidy every module so go.sum exists (the Docker builds need it):
for d in pkg gen/go services/*; do (cd "$d" && go mod tidy); done
go test ./...              # fix whatever the first real compile surfaces, then commit
git add gen/ **/go.sum
git commit -m "Add generated code + go.sum"
git push
```
> The Dockerfiles copy `gen/` and run `go build`; they do **not** run Buf. So `gen/go`
> must be committed. Expect the first `go test ./...` to surface a handful of
> import/gofmt/tidy fixes — that's normal for a fresh 18-module monorepo.

## 1. Database — one per service (important)
Every service runs its own goose migrations, and several define tables with the
same name (`outbox`, `processed_events`) plus goose's own `goose_db_version`.
**Sharing one database collides.** After Render creates `restorna-postgres`, run
the init script once via its External Connection string:

```bash
psql "$RENDER_EXTERNAL_DATABASE_URL" -f deploy/postgres/init-databases.sql
```
This creates `restorna_identity`, `restorna_tenant`, … (one per service). Then set
each service's `DATABASE_URL` to its own db name (same host, different database):
`postgres://USER:PASS@HOST:5432/restorna_<service>`. Connect as a **non-superuser,
non-BYPASSRLS** role so RLS is enforced (see the note in the init script; platform
`ListOwners` needs a BYPASSRLS role or an admin policy).

## 2. Secrets (set in the Render dashboard / a secret group)
- `JWT_PUBLIC_KEY` — base64 Ed25519 public key (all services verify with it).
- `JWT_PRIVATE_KEY` — base64 Ed25519 private key (**identity only**, it signs).
- `CONNECTOR_KEK` — base64 32-byte AES key (**connectorhub only**, encrypts connector secrets).

Generate a fresh set with:
```bash
# one-liner (needs node, or use openssl/a Go snippet)
node -e 'const c=require("crypto");const{publicKey,privateKey}=c.generateKeyPairSync("ed25519");const pub=publicKey.export({type:"spki",format:"der"}).subarray(-32);const seed=privateKey.export({type:"pkcs8",format:"der"}).subarray(-32);console.log("JWT_PUBLIC_KEY="+pub.toString("base64"));console.log("JWT_PRIVATE_KEY="+Buffer.concat([seed,pub]).toString("base64"));console.log("CONNECTOR_KEK="+c.randomBytes(32).toString("base64"))'
```
A **dev-only** sample set (do NOT use in production) is in `.env.sample`.

## 3. Cost / topology
18 web services on Render is ~$126/mo on the paid tier (or it exceeds free-tier
limits and everything cold-starts). Recommended: keep the **gateway** and the
**console** public, and flip the other 16 services to **private** (`type: pserv`)
so only the edge is exposed. Public provider webhooks reach `connectorhub`'s
`IngestWebhook` **through the gateway**, so nothing else needs to be public.

## 4. Deploy
1. Push the repo (with committed `gen/` + `go.sum`) to `github.com/abhi-codeabhi/oms`.
2. Render → New → Blueprint → pick the repo → it reads `render.yaml`.
3. Set the three secrets above; set each service's `DATABASE_URL` to its own db.
4. Set the console's `VITE_GATEWAY_URL` to the gateway's public URL.
5. Deploy. Health: every service exposes `/healthz` and `/readyz`.

## 5. Verify
- `GET https://<gateway>/healthz` → ok.
- `POST /api/auth/start-otp` then `/api/auth/verify-otp` (dev OTP `123456` when
  `APP_ENV=dev`) → token.
- Open the console → run the owner onboarding wizard (account → plan → brand+logo →
  outlet → team → go-live).

> Same images run on AWS (EKS/ECS), GCP (GKE/Cloud Run), Azure (AKS) — see DEPLOY.md.
> For an all-in-one alternative, `deploy/docker-compose.yml` runs the infra locally.
