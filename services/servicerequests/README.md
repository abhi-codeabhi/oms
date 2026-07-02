# servicerequests service

Data-plane (OMS) service: the diner's **"call waiter / water / bill / cutlery"**
requests, with **escalation** when overdue and a **per table+type cooldown**
(rate limit). A request's lifecycle is **assigned → escalated → done**.

Ported from the proven Restorna Node `service-requests`:
- **raise** with a per **table+type** cooldown (re-raising the same table+type
  within the cooldown window is rejected),
- **escalateDue** flips assigned requests past a threshold to escalated,
- **acknowledge** completes a request and **records the cooldown** anchor,
- **listOpen** returns everything not yet `done`.

Hexagonal (`domain → app → ports → adapters`), per `CONVENTIONS.md`.

## RPCs (`restorna.servicerequests.v1.ServiceRequestsService`)

| RPC | Notes |
|-----|-------|
| `Raise` | Create a request for a table. **FailedPrecondition** if the same `table`+`type` was acknowledged within the cooldown window. A request raised with an `assigned_to` starts `assigned`; an unassigned one starts `escalated` (nobody owns it yet). Emits `raised.v1`. |
| `ListOpen` | Every request **not yet done** (`assigned` + `escalated`), oldest first. |
| `Acknowledge` | Mark one request `done` and **record the table+type cooldown** (`acked_at`) so the guest cannot immediately re-raise the same request. |
| `EscalateDue` | Sweep: flip every **assigned** request older than the escalation threshold to `escalated`. `now` (epoch ms) drives the sweep deterministically; `0` uses the server clock. Emits `escalated.v1` per request. |

## Thresholds (from SettingsService)

Resolved per restaurant via **`SettingsService.GetEffective`** (the
`ports.SettingsResolver` port; unit tests use a fake):

| Setting key | Meaning | Default |
|-------------|---------|---------|
| `floor.call.cooldown_secs` | Rate-limit window for re-raising the same table+type after an acknowledge | **60s** |
| `floor.call.escalate_secs` | How long an assigned request may wait before escalation | **30s** |

If settings is unavailable (or a value is missing/malformed) the service
**degrades to the defaults** (60s / 30s) rather than failing the request.
`SETTINGS_URL` selects the endpoint.

## Events emitted (outbox → NATS, CloudEvents envelope)

- `restorna.servicerequests.raised.v1` — `{request_id, type, table, state, assigned_to}`
  — billing consumes this to mark a table **"asked"** when `type == "bill"`.
- `restorna.servicerequests.escalated.v1` — `{request_id, type, table}` — one per
  request flipped by `EscalateDue`.

Staged in the **same transaction** as the write (`pkg/outbox.Stage`); a relay
drains to NATS.

## Multi-tenancy

Per-outlet: the tenant key is `restaurant_id`. Tables `requests`, `cooldowns`
(+ `outbox`) carry `restaurant_id`/`tenant_id` with **Postgres RLS** scoped to
`current_setting('app.tenant_id')`, set per transaction by `pkg/pg.WithTenant`.
The restaurant id always comes from the JWT-derived `pkg/tenancy` scope — never a
request body.

## Layout

```
cmd/server/main.go                 composition root (grpc + outbox relay)
internal/
  domain/                          pure Request aggregate + cooldown/escalation rules (no infra)
  app/                             use cases: Raise, ListOpen, Acknowledge, EscalateDue
  ports/                           Repository, SettingsResolver interfaces
  adapters/
    pg/                            Postgres repo + RLS + outbox staging (requests, cooldowns)
    grpc/                          Connect handler (proto <-> domain, error mapping)
    settings/                      SettingsService client (ports.SettingsResolver impl)
migrations/                        goose SQL (requests, cooldowns, outbox, RLS)
*_test.go                          table-driven unit tests (domain + app, in-memory fakes)
```

## Build / test / run

```bash
go test ./...                      # unit tests (domain + app), no DB needed

# container (build context = repo root so go.mod replaces resolve)
docker build -f services/servicerequests/Dockerfile -t restorna/servicerequests .
```

### Env

`PORT`, `DATABASE_URL`, `NATS_URL`, `JWT_PUBLIC_KEY`, `OTEL_EXPORTER_OTLP_ENDPOINT`
(shared base) plus `SETTINGS_URL` (SettingsService endpoint for the thresholds).

> Generated proto/Connect code lives in `gen/go/restorna/servicerequests/v1`
> (+ `servicerequestsv1connect`). Run `buf generate` / `go test ./...` on a
> machine with the Go toolchain — none is available in the authoring environment.
