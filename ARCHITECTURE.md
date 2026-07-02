# Restorna Platform — Architecture Blueprint

A multi-tenant restaurant-OS **SaaS platform**. Beyond the single-restaurant OMS,
this product adds a **control plane** (client onboarding, brands, outlets, plans,
staff limits, branding) and a **plug-and-play integration plane** (payments,
aggregators, CRM/ERP) over a performant **Go microservices** core.

Decisions locked: **Go** services · **monorepo** (Buf + Go workspaces) ·
**control plane first** · **Kubernetes, cloud-agnostic**.

---

## 1. Product shape & tenancy hierarchy

```
Platform (Restorna, the SaaS operator)         ← control plane / platform admin
└── Owner (customer account, billable)          ← signs up, onboarded with logo
    └── Brand (e.g. "Burger Co", "Curry House")  ← one owner → many brands
        └── Outlet / Restaurant (a location)      ← one brand → many outlets
            ├── Staff (manager/waiter/kitchen/cashier) — limited by plan
            ├── Tables, Menu (brand menu + outlet overrides)
            └── Orders, Bills, KDS, Floor          ← the OMS data plane
```

Every operational record carries a **tenant scope**: `owner_id → brand_id →
restaurant_id`. The active `restaurant_id` (or `brand_id` for brand-level config)
is the tenancy key propagated through every call.

**Roles** (RBAC, scoped):
- `platform_admin` — Restorna staff; cross-tenant, control-plane only.
- `owner` — full control of their brands/outlets; manages billing & staff.
- `brand_admin` — manages one brand (menu, outlets, promos).
- `manager` — runs one outlet (staff, floor, menu availability, nudges).
- `waiter`, `kitchen`, `cashier`/`billing` — operational personas.
- `customer` — anonymous, QR table session (no login).

---

## 2. Control plane vs data plane vs integration plane

| Plane | Responsibility | Services |
|------|----------------|----------|
| **Control** | who exists, what they're allowed to do, branding, money owed to *us* | identity, tenant, entitlements, staff, onboarding, billing-saas |
| **Data (OMS)** | running the restaurant | catalog, ordering, kitchen, floor, billing-oms, promotions, service-requests |
| **Integration** | connecting to the outside world | connector-hub, payments, aggregators, notifications, analytics |
| **Edge** | auth, routing, per-surface shaping | api-gateway + BFFs (platform / owner / staff / customer) |

Separation matters: the control plane changes slowly and is high-trust; the data
plane is high-throughput and per-outlet; the integration plane is where vendors
plug in. Each scales and fails independently.

---

## 3. Service catalog (each = own Go module, own Postgres, own gRPC API + events)

### Control plane
1. **identity** — users, credentials, OTP (SMS/email), SSO/OIDC, sessions, JWT
   issuance (access+refresh), API keys. Realms: platform vs tenant.
2. **tenant** — owner→brand→restaurant hierarchy, provisioning, **branding/logo**
   (asset refs), addresses, tax profile (GSTIN), operating hours.
3. **entitlements** — plans (Free/Growth/Pro/Enterprise), **quotas/limits**
   (max outlets, max staff per role, max tables), feature flags. Single source of
   "are you allowed N more waiters?".
4. **staff** — roster, role assignment, invitations, RBAC bindings; **enforces
   limits** by checking entitlements before adding staff.
5. **onboarding** — orchestrates the signup→provision→go-live workflow (saga):
   create owner, brand, first outlet, seed menu, generate table QRs, invite team.
6. **billing-saas** — subscriptions & invoicing for what owners pay **Restorna**
   (Stripe/Razorpay Subscriptions); dunning; plan changes drive entitlements.

### Data plane (OMS — port the proven Restorna domain)
7. **catalog** — categories, items, modifiers, dietary tags; brand menu + outlet
   overrides; availability/86; menu versioning + publish.
8. **ordering** — orders, table sessions, multi-round dine-in lifecycle.
9. **kitchen** — KDS tickets, station routing, cook→ready→served per ticket.
10. **floor** — tables, seating, waiter assignment, derived status, nudges.
11. **billing-oms** — table bills, GST, splits, payment capture, settlement.
12. **promotions** — coupons, happy hour, scheduled discounts.
13. **service-requests** — call waiter/water/bill, escalation, rate limit.

### Integration plane
14. **connector-hub** — the plug-and-play framework: connector registry, per-tenant
    config, capability routing, inbound webhook ingestion, outbound dispatch,
    retries/DLQ. Connectors implement a `Connector` interface + manifest.
15. **payments** — payment orchestration: one API over Razorpay/Paytm/PhonePe/UPI;
    idempotent intents, webhooks, refunds, reconciliation. (Customer-facing money.)
16. **aggregators** — Zomato/Swiggy menu sync + order ingestion (via connector-hub
    adapters) into ordering.
17. **notifications** — SMS/WhatsApp/email/push (Twilio/MSG91/FCM) behind one API.
18. **analytics** — CQRS read models for owner/brand dashboards, menu engineering.

### Edge
19. **gateway** — Connect/gRPC-Gateway edge: authN (verify JWT), authZ scoping,
    rate limit, request shaping, and thin **BFFs** per surface (platform console,
    owner console, manager/staff PWA, customer QR).

> The existing React surfaces (customer/kitchen/waiter/billing/manager/owner) are
> reused as the **staff/owner web apps**; a new **platform console** is added.

---

## 4. Communication

- **Synchronous (request/response):** **gRPC** with **Connect-Go** (gRPC + gRPC-Web
  + JSON over HTTP/1.1 from one handler — great for browser BFFs without a proxy).
  Contracts in **Protobuf**, managed by **Buf** (lint + breaking-change checks +
  codegen). Service-to-service is gRPC; the edge exposes Connect/JSON to browsers.
- **Asynchronous (events):** **NATS JetStream** (default; lighter than Kafka, easy
  on K8s; swap to Kafka at scale). Every state change emits a **domain event** via
  the **transactional outbox** pattern (write + event in one DB tx; a relay
  publishes). Events use a **CloudEvents** envelope; consumers are **idempotent**
  (dedupe on event id). Examples: `tenant.outlet.provisioned`,
  `ordering.order.placed.v1`, `payments.captured.v1`.
- **"Seamless inter-service" =** typed gRPC clients generated from the shared proto
  + a service registry via K8s DNS (+ optional **service mesh**, Linkerd, for mTLS,
  retries, and golden metrics without app code).

Why both: gRPC for low-latency queries/commands; events for decoupling, fan-out,
and resilience (a slow connector never blocks an order).

---

## 5. Data architecture & multi-tenancy

- **Database-per-service** (Postgres). No service reads another's tables; they call
  its API or consume its events. This is what makes services independently
  deployable and scalable.
- **Multi-tenancy tiers** (per the data's sensitivity/scale):
  - **T1 — pooled + RLS (default):** shared tables, `tenant_id` column, Postgres
    **Row-Level Security** scoped by `current_setting('app.tenant_id')`. Tenant id
    set per transaction from the gRPC auth context. Cheapest, fine for most.
  - **T2 — schema-per-tenant:** for larger outlets/brands needing isolation.
  - **T3 — database-per-tenant:** enterprise/regulatory; same code, different DSN
    resolved by a tenant→shard map.
- **Outbox** table per service for reliable publishing; a relay drains to NATS.
- **CQRS / read models** where reads dwarf writes (analytics, dashboards, KDS
  boards) — projections built from events into read-optimized stores.
- **Caching:** Redis for sessions, rate-limit counters, hot menu, entitlement
  checks.
- **Money** is always integer **minor units** (paise) + currency; never floats.
- **IDs:** ULIDs (sortable, k-sortable) prefixed by type (`out_`, `usr_`, `ord_`).

---

## 6. Plug-and-play integration framework (the differentiator)

A connector is a self-contained module implementing a small interface, declared by
a **manifest** (id, capabilities, config schema, webhook routes). The **connector-hub**
discovers, configures (per tenant), and routes to them — adding Razorpay or Zomato
is *additive*, no core changes.

```go
// pkg/connector — the contract every integration implements.
type Capability string // "payment" | "aggregator" | "crm" | "erp" | "notification"

type Connector interface {
    Manifest() Manifest                  // id, name, capabilities, config schema
    Init(ctx, TenantConfig) error        // per-tenant credentials/options
    Capabilities() []Capability
}

type PaymentConnector interface {
    Connector
    CreateIntent(ctx, PaymentIntent) (ProviderRef, error)
    Capture(ctx, ProviderRef) (Receipt, error)
    Refund(ctx, ProviderRef, MoneyMinor) (Refund, error)
    HandleWebhook(ctx, Signed[]byte) (Event, error) // verify + normalize
}

type AggregatorConnector interface {
    Connector
    PushMenu(ctx, Menu) error
    PullOrders(ctx) ([]ExternalOrder, error)        // or webhook-driven
    AckOrder(ctx, ExternalOrderRef, Status) error
}
```

- **payments** service uses `PaymentConnector` adapters (Razorpay/Paytm/PhonePe);
  one internal API; per-tenant gateway selection + fallback; idempotent intents;
  signed-webhook ingestion; reconciliation jobs.
- **aggregators** use `AggregatorConnector` (Zomato/Swiggy): menu push + order
  ingestion → `ordering`.
- **CRM/ERP** connectors implement `crm`/`erp` capabilities (Salesforce/HubSpot,
  or a dedicated Restorna CRM/ERP) — outbound event sync + inbound sync.
- Config & secrets per tenant in `connector-hub` (encrypted), gated by
  **entitlements** (a plan unlocks which connectors are available).

---

## 7. Entitlements & limits (how "add limit to manage/waiter/staff" works)

`entitlements` owns plans → quotas. Any service about to create a constrained
resource asks: `CheckQuota(tenant, "staff.waiter", +1)`. Example: `staff.AddStaff`
calls `entitlements.CheckAndReserve` first; over-limit → `RESOURCE_EXHAUSTED` with
an upgrade hint surfaced to the owner. Quotas: `outlets`, `staff.<role>`, `tables`,
`connectors.<id>`, plus boolean **feature flags** (e.g. `aggregators`, `analytics_pro`).
Plan changes (from `billing-saas`) update entitlements; everything else reads them.

---

## 8. Security & auth

- **identity** issues short-lived **JWT** access tokens (5–15 min) + rotating
  refresh tokens. Owners/platform: OTP + optional **OIDC SSO**. Customers: anonymous
  QR session tokens scoped to one table.
- **AuthZ** = RBAC + tenancy scope claims in the JWT (`sub`, `role`, `owner_id`,
  `brand_id`, `restaurant_id`). gRPC **interceptors** validate the token, load the
  tenancy context, and enforce role → set `app.tenant_id` for RLS.
- **Platform realm** is separate (no tenant can reach control-plane admin APIs).
- **Service-to-service:** mTLS (mesh) + signed internal claims.
- **Secrets:** cloud secret manager / Vault; connector credentials encrypted at rest
  (envelope encryption), never logged.

---

## 9. Observability & reliability

- **OpenTelemetry** traces/metrics/logs; **Prometheus + Grafana**; structured logs
  (zerolog) with correlation + tenant ids. RED/USE dashboards per service.
- **Resilience:** outbox + idempotent consumers; retries with backoff + **DLQ**;
  circuit breakers on connector calls; timeouts/deadlines propagated via context;
  graceful shutdown; health/readiness probes.

---

## 10. Deployment (Kubernetes, cloud-agnostic)

- One **container image per service** (distroless, multi-stage Go build, non-root).
- **Helm** chart per service + an umbrella chart; **Kustomize** overlays per env
  (dev/staging/prod). Runs on **any** managed K8s (EKS/GKE/AKS) or self-hosted — no
  cloud-specific primitives in app code (object storage, queues abstracted behind
  interfaces).
- **Ingress** (nginx/traefik) → gateway. **HPA** on CPU/RPS. **PodDisruptionBudgets**.
- **Infra dependencies** (Postgres, NATS, Redis) via operators or managed equivalents,
  selected per environment.
- **CI/CD:** GitHub Actions — `buf lint` + `buf breaking`, `go vet`/`golangci-lint`,
  `go test ./...`, build/scan/push images, Helm deploy via Argo CD/Flux (GitOps).
- **Local dev:** `deploy/docker-compose.yml` (Postgres, NATS, Redis, Jaeger) + Tilt
  or Skaffold against `kind` for the full mesh.

---

## 11. Repository layout (monorepo)

```
restorna-platform/
├── go.work                      # Go workspace ties all service modules together
├── buf.work.yaml, buf.gen.yaml  # Buf module + codegen config
├── Makefile                     # gen / build / test / lint / up / migrate
├── ARCHITECTURE.md, CONVENTIONS.md, README.md
├── proto/restorna/              # ALL contracts (one Buf module)
│   ├── common/v1/               # money, ids, tenancy, pagination, errors
│   ├── identity/v1/  tenant/v1/  entitlements/v1/  staff/v1/  onboarding/v1/
│   ├── catalog/v1/  ordering/v1/ kitchen/v1/ floor/v1/ billing/v1/ ...
│   └── connector/v1/  payments/v1/ events/v1/
├── gen/go/                       # generated Go (Connect + messages)
├── pkg/                          # shared libraries (no business logic)
│   ├── auth/ tenancy/ money/ ids/ errors/ outbox/ events/ config/
│   ├── logging/ otel/ grpcx/ (interceptors) connector/ (the SDK) pg/ (db helpers)
├── services/<name>/             # one per service
│   ├── go.mod  cmd/server/main.go
│   └── internal/{domain, app, ports, adapters}  + migrations/  + *_test.go
├── deploy/
│   ├── docker-compose.yml        # local infra
│   ├── helm/<service>/ + helm/umbrella/
│   └── k8s/ (kustomize base + overlays)
├── web/                          # platform-console (new) + reuse existing React apps
└── .github/workflows/ci.yml
```

Each `services/<name>` is **hexagonal**: `domain` (pure), `app` (use cases / ports),
`adapters` (postgres, grpc, nats), `ports` (interfaces). Same shape everywhere so an
agent can build any service from the template.

---

## 12. Milestone roadmap & agent workstreams

- **M0 — Foundation** *(lead-authored; this document + scaffolding)*: repo, Buf,
  go.work, `pkg/` shared libs, common proto, docker-compose, Helm/CI skeleton,
  CONVENTIONS.md, a reference service template.
- **M1 — Control plane** *(first; parallel agents)*:
  - `identity` · `tenant` (hierarchy + branding/logo) · `entitlements` (plans/limits)
    · `staff` (RBAC + limit enforcement) · `onboarding` (provisioning saga) ·
    `gateway` + owner/platform BFFs · **platform-console** web app.
- **M2 — Data plane (OMS port)**: catalog, ordering, kitchen, floor, billing-oms,
  promotions, service-requests — porting the proven domain to Go, multi-tenant.
- **M3 — Integration plane**: connector-hub + payments (Razorpay/Paytm/PhonePe) +
  aggregators (Zomato/Swiggy) + notifications.
- **M4 — Analytics, CRM/ERP connectors, scale & hardening.**

**Parallelization:** because every service implements a **fixed proto contract** and
the shared `pkg/` interfaces, services are built independently and concurrently by
separate agents. Contracts are authored **first** (M0) so agents never block on each
other. Integration = wiring generated clients + event subscriptions, validated by
contract tests.

---

## 13. Why this is "scalable, modular, plug-and-play"

- **Scalable:** stateless Go services + DB-per-service + events; scale hot services
  (ordering, payments) independently via HPA; tenancy tiers for big customers.
- **Modular:** bounded contexts behind gRPC contracts; hexagonal internals; swap any
  adapter (DB, broker, gateway) without touching the domain.
- **Plug-and-play:** the connector SDK makes payments/aggregators/CRM/ERP additive,
  gated by entitlements — new revenue lines without core changes.
- **Cloud-agnostic:** one image per service, vanilla K8s + Helm, infra behind
  interfaces — runs anywhere, no lock-in.
