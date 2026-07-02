# identity service

Control-plane service that authenticates principals and issues JWTs for the
Restorna Platform. **Cross-tenant**: a user is not bound to an owner/brand/
restaurant — tenant scope is attached only when a *scoped* token is minted.
Realms separate `PLATFORM` (Restorna staff) from `TENANT` (owners/staff);
customers get anonymous QR session tokens.

Hexagonal layout (per `CONVENTIONS.md`): `domain` (pure) → `app` (use cases on
ports) → `adapters` (pg / grpc / sender / auth) wired in `cmd/server/main.go`.

## RPCs (`restorna.identity.v1.IdentityService`)

| RPC | Purpose |
|-----|---------|
| `StartOtp` | Validate channel+address, create a TTL-bound OTP challenge (random 6-digit code), hand the code to the pluggable `Sender`. Returns `challenge_id`. |
| `VerifyOtp` | Check the code (attempt-limited), auto-register the user on first login, consume the challenge, issue an **unscoped** access+refresh `TokenPair` + the `User`. |
| `Refresh` | Exchange a valid refresh token for a new pair; **rotates** (revokes the old, preserves role+scope). |
| `IssueScopedToken` | Mint a token narrowed to a concrete `TenantRef` + `Role` for an existing user (e.g. owner picks an outlet). |
| `CustomerSession` | Anonymous `ROLE_CUSTOMER` token bound to `restaurant_id` + `table` (from the QR). No user row created. |
| `Introspect` | Verify an access token (used by the gateway). Invalid/expired ⇒ `{active:false}`, not an error. |

## Auth & tokens

- **Ed25519 JWT** via `pkg/auth`. Short-lived **access** tokens (15 min) +
  rotating **refresh** tokens (30 days). Refresh tokens are stored as a
  SHA-256 **hash**, never plaintext.
- Claims carry `sub (user)`, `role`, and tenant scope (`owner/brand/restaurant`).
- The service holds the **private** key (`JWT_PRIVATE_KEY`, base64 std) to sign;
  the **public** key (`JWT_PUBLIC_KEY`) is shared with every service's auth
  interceptor to verify.

## OTP

- Challenge stored with a **TTL** (5 min) + **attempt limit** (5). Wrong guesses
  increment the counter; hitting the limit locks the challenge.
- Delivery is a pluggable **`Sender`** port. Ships with a **log/no-op** impl; the
  real SMS/email path is the **notifications** service later.
- **DEV mode** (`APP_ENV=dev`): the code **`123456`** always verifies, so local
  flows don't need a real channel.

## Config (12-factor)

| Env | Meaning |
|-----|---------|
| `PORT` | HTTP/2 listen port (default 8080). |
| `DATABASE_URL` | Postgres DSN. |
| `JWT_PUBLIC_KEY` | Ed25519 public key (base64), shared verify key. |
| `JWT_PRIVATE_KEY` | Ed25519 private key (base64), issuer signing key. |
| `APP_ENV` | `dev` enables the dev OTP code. |
| `NATS_URL`, `OTEL_EXPORTER_OTLP_ENDPOINT` | per `pkg/config.Base`. |

## Data model (goose migrations, cross-tenant — no `tenant_id`/RLS)

- `users` — `realm`-scoped principals (`usr_…`), unique address per realm.
- `otp_challenges` — pending verifications with `expires_at` + `attempts`.
- `refresh_tokens` — rotating tokens by `token_hash`, with role + optional scope.

## Build / run / test

```bash
# from repo root (workspace ties in ./pkg and ./gen/go)
go test ./services/identity/...
go build ./services/identity/...

# container (build context = repo root)
docker build -f services/identity/Dockerfile -t restorna/identity .
```

> Codegen (`buf generate`) must have populated `gen/go/restorna/{identity,common}/v1`
> before building.

## Events

Emits `restorna.identity.user.registered.v1` (outbox) — wiring lands with the
event/outbox pass.
