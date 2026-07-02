# Deploying the Restorna Platform

The platform is **cloud-agnostic by construction**: one container image per
service, every service is **12-factor** (all config via env vars, binds `$PORT`),
and there is **no provider SDK or cloud primitive in application code**. The same
images run on Render, any managed Kubernetes (EKS/GKE/AKS), serverless containers
(ECS Fargate / Cloud Run), or a vanilla self-hosted cluster.

> Principle: **the image you test in CI is the image you ship everywhere.** Only
> the surrounding infra (Postgres, NATS, Redis) and the env-var values change.

---

## 1. The artifacts

All **18 services** ship the same way — one Dockerfile per service, built from the
repo root. The platform is three milestones (plus a React web console served as a
static site):

- **M1 — control plane (7):** identity, tenant, entitlements, staff, settings,
  onboarding, gateway.
- **M2 — Order Management System / OMS data plane (7):** catalog, ordering,
  kitchen, floor, billing, promotions, servicerequests.
- **M3 — integration plane (4):** connectorhub, payments, notifications,
  aggregators.

| Service | Dockerfile | Default port | Notes |
|---|---|---|---|
| identity | `services/identity/Dockerfile` | `$PORT` (8080) | signs JWTs — needs `JWT_PRIVATE_KEY` |
| tenant | `services/tenant/Dockerfile` | `$PORT` (8080) | |
| entitlements | `services/entitlements/Dockerfile` | `$PORT` (8080) | |
| staff | `services/staff/Dockerfile` | `$PORT` (8080) | |
| settings | `services/settings/Dockerfile` | `$PORT` (8080) | config catalog; OMS reads it |
| onboarding | `services/onboarding/Dockerfile` | `$PORT` (8080) | saga orchestrator |
| gateway | `services/gateway/Dockerfile` | `$PORT` (8080) | internet-facing edge + BFFs |
| catalog | `services/catalog/Dockerfile` | `$PORT` (8080) | OMS — DB + NATS only |
| ordering | `services/ordering/Dockerfile` | `$PORT` (8080) | OMS — DB + NATS only |
| kitchen | `services/kitchen/Dockerfile` | `$PORT` (8080) | OMS — reads `CATALOG_URL` |
| billing | `services/billing/Dockerfile` | `$PORT` (8080) | OMS — reads ordering, catalog, settings, promotions |
| floor | `services/floor/Dockerfile` | `$PORT` (8080) | OMS — reads kitchen, billing, settings, ordering |
| promotions | `services/promotions/Dockerfile` | `$PORT` (8080) | OMS — DB + NATS only |
| servicerequests | `services/servicerequests/Dockerfile` | `$PORT` (8080) | OMS — reads `SETTINGS_URL` |
| connectorhub | `services/connectorhub/Dockerfile` | `$PORT` (8080) | M3 — connector registry + config + webhook ingestion; reads `ENTITLEMENTS_URL`; **needs `CONNECTOR_KEK`** |
| payments | `services/payments/Dockerfile` | `$PORT` (8080) | M3 — money over any gateway; reads `CONNECTORHUB_URL` |
| notifications | `services/notifications/Dockerfile` | `$PORT` (8080) | M3 — SMS/WhatsApp/email/push; reads `CONNECTORHUB_URL` |
| aggregators | `services/aggregators/Dockerfile` | `$PORT` (8080) | M3 — Zomato/Swiggy; reads `CONNECTORHUB_URL`, `CATALOG_URL`, `ORDERING_URL` |

Plus the **web console** (`web/console/`) — a React + Vite SPA built to static
assets (`npm run build` → `dist`) and served from any CDN / static host. It is not
a Go service; see §3 (Render) and §4-notes below.

> **Event bus is a hard dependency for the OMS.** The seven M2 services
> choreograph the order→kitchen→floor→billing lifecycle over **NATS/JetStream**
> (publish + consume domain events through `pkg/events` + the transactional
> outbox); `NATS_URL` is therefore required for every M2 service, not optional.
> The `*_URL` vars above are the synchronous request paths layered on top.

All Dockerfiles **build from the repo root** as context because each service
module depends on sibling modules `./pkg` and `./gen/go` (wired by `go.work` and
`replace` directives):

```bash
docker build -f services/identity/Dockerfile -t ghcr.io/restorna/identity:dev .
```

Images are distroless, non-root (UID 65532), static binaries.

---

## 2. The env-var matrix (12-factor)

Every service reads the same base config (`pkg/config.Base`):

| Env var | Required | Meaning | Source |
|---|---|---|---|
| `PORT` | injected | listen port (default 8080) | platform |
| `DATABASE_URL` | yes | Postgres DSN | managed DB or in-cluster |
| `NATS_URL` | yes | NATS/JetStream URL (`nats://nats:4222`) | managed or in-cluster |
| `JWT_PUBLIC_KEY` | yes | Ed25519 public key (base64) — shared verify key | secret |
| `JWT_PRIVATE_KEY` | identity only | Ed25519 private key (base64) — signing key | secret |
| `APP_ENV` | no (`dev`) | `dev`/`staging`/`production` | config |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | no | OTLP collector for traces | config |
| `IDENTITY_URL`, `TENANT_URL`, `ENTITLEMENTS_URL`, `STAFF_URL`, `SETTINGS_URL`, `ONBOARDING_URL` | as needed | M1 inter-service base URLs | configmap / service discovery |
| `CATALOG_URL`, `ORDERING_URL`, `KITCHEN_URL`, `BILLING_URL`, `PROMOTIONS_URL` | as needed | M2/OMS inter-service base URLs | configmap / service discovery |
| `CONNECTORHUB_URL` | M3 (payments/notifications/aggregators) | connector-hub base URL (Resolve the active provider + decrypted config) | configmap / service discovery |
| `CONNECTOR_KEK` | **connectorhub only** | 32-byte Key-Encryption-Key (base64/hex/raw) sealing secret connector config at rest (AES-256-GCM). connector-hub **refuses to start without it** | **secret** |

OMS inter-service wiring (what each M2 service consumes synchronously):

| Service | Reads |
|---|---|
| catalog, ordering, promotions | _(none — DB + NATS only)_ |
| kitchen | `CATALOG_URL` |
| billing | `ORDERING_URL`, `CATALOG_URL`, `SETTINGS_URL`, `PROMOTIONS_URL` |
| floor | `KITCHEN_URL`, `BILLING_URL`, `SETTINGS_URL`, `ORDERING_URL` |
| servicerequests | `SETTINGS_URL` (reads `floor.call.escalate_secs`) |

M3 integration-plane wiring (each read from the service's `cmd/server/main.go` `Config`):

| Service | Reads | Secret |
|---|---|---|
| connectorhub | `ENTITLEMENTS_URL` | **`CONNECTOR_KEK`** (in `connectorhub-secrets`) |
| payments | `CONNECTORHUB_URL` | — |
| notifications | `CONNECTORHUB_URL` | — |
| aggregators | `CONNECTORHUB_URL`, `CATALOG_URL`, `ORDERING_URL` | — |

> **Public webhook edge.** Provider webhooks (payment captures, aggregator
> orders, delivery reports) hit connector-hub's `IngestWebhook`, which verifies
> the provider signature via the connector's `VerifyWebhook`, normalizes to a
> CloudEvent, and publishes it to NATS (payments/notifications/aggregators consume
> it). Expose this **through the gateway** — it is the only public path into the
> integration plane; keep the M3 services themselves private.

Health endpoints (served by `pkg/grpcx`): **`/healthz`** (liveness) and
**`/readyz`** (readiness).

**Secrets** (`DATABASE_URL`, `NATS_URL`, `JWT_*`) are never committed. Inject them
via the platform's secret store:
- Render: env groups / `sync: false` vars set in the dashboard.
- Kubernetes: a `Secret` named `<svc>-secrets` (Helm/Kustomize reference it via
  `secretRef`). Manage with sealed-secrets, external-secrets, or `kubectl`.
- ECS/Cloud Run: Secrets Manager / Secret Manager bound as env.

**Migrations** run on service startup (`pg.Migrate` with the embedded goose FS in
each service's `migrations/`), so no separate migration job is required. To run
them out-of-band instead, use `make migrate` (goose) as a Job/one-off task before
rollout.

---

## 3. Render (PaaS, no Kubernetes)

`render.yaml` (repo root) is a complete Blueprint: **all 18 services** (7 M1 +
7 M2/OMS + 4 M3 integration) as Docker web services (`dockerfilePath` per service,
`dockerContext: .`), a **managed Postgres**, a **private NATS** service
(`nats:2-alpine` with `-js`), and the **web console** as a Render **static site**
(`runtime: static`, `rootDir: web/console`, `buildCommand: npm install && npm run
build`, `staticPublishPath: dist`, SPA rewrite `/* → /index.html`). Inter-service
URLs are wired with `fromService … property: hostport`; `DATABASE_URL` with
`fromDatabase`. Every service carries `NATS_URL=nats://nats:4222` — required for
the OMS + integration-plane event choreography.

M3 wiring: connectorhub reads `ENTITLEMENTS_URL` (fromService) and gets
`CONNECTOR_KEK` via `generateValue: true` (Render generates + stores a strong
value); payments/notifications point `CONNECTORHUB_URL` at connectorhub;
aggregators also reads `CATALOG_URL` + `ORDERING_URL`. The console's
`VITE_GATEWAY_URL` (`sync:false`) is baked into the bundle at build time — set it
to the gateway's public URL and add the console origin to the gateway's
`CORS_ALLOWED_ORIGINS`.

```bash
# Push the repo, then in Render: New → Blueprint → pick the repo.
# Set JWT_PUBLIC_KEY / JWT_PRIVATE_KEY (sync:false) in the dashboard.
# Set the console's VITE_GATEWAY_URL (sync:false) to the gateway's public URL.
# CONNECTOR_KEK is auto-generated (generateValue) — rotate via the dashboard.
```

Proves the platform runs without K8s. Redis: add a Render Key-Value (Redis) and
set `REDIS_URL` if/when a service needs it. (A standalone
`web/console/render.yaml` also exists for deploying the console on its own.)

---

## 4. Any vanilla Kubernetes

Two interchangeable paths ship in `deploy/`:

### Helm (`deploy/helm/`)
- One chart per service (`deploy/helm/<svc>/`) for **all 18 services**:
  Deployment + Service + HPA + ConfigMap + optional Secret, probes on
  `/healthz` `/readyz`, resource requests/limits. Inter-service URLs are
  set per chart in `values.yaml` under `serviceURLs` (cluster DNS,
  `http://<svc>:8080`). The M3 `connectorhub-secrets` Secret must additionally
  carry `CONNECTOR_KEK` (kept out of the ConfigMap by design).
- Umbrella chart (`deploy/helm/umbrella/`) depends on all service charts and
  optional in-cluster **Postgres/NATS/Redis** subcharts (Bitnami / nats-io).
  Disable the infra subcharts to use managed equivalents.

```bash
# in-cluster infra:
helm dependency build deploy/helm/umbrella
helm upgrade --install restorna deploy/helm/umbrella -n restorna --create-namespace

# managed infra: turn off subcharts, point services at managed endpoints
helm upgrade --install restorna deploy/helm/umbrella -n restorna \
  --set postgresql.enabled=false --set nats.enabled=false --set redis.enabled=false
# (provide <svc>-secrets with DATABASE_URL/NATS_URL out-of-band)
```

### Kustomize (`deploy/k8s/`)
- `base/` holds plain manifests (Deployment/Service/HPA/ConfigMap + gateway
  Ingress) for **all 18 services**; inter-service URLs live in each
  `<svc>-config.yaml` ConfigMap (cluster DNS). `CONNECTOR_KEK` is **not** in
  `connectorhub-config.yaml` — it comes from the `connectorhub-secrets` Secret.
  Overlays pin a per-environment image tag (`newTag`) for every service.
- `overlays/{dev,staging,prod}` patch **image tags, replica counts, and the
  ingress host**.

```bash
kubectl apply -k deploy/k8s/overlays/prod
```

Create the `<svc>-secrets` Secret in each namespace first:
```bash
kubectl -n restorna-prod create secret generic identity-secrets \
  --from-literal=DATABASE_URL=postgres://… \
  --from-literal=NATS_URL=nats://nats:4222 \
  --from-literal=JWT_PUBLIC_KEY=… --from-literal=JWT_PRIVATE_KEY=…

# connectorhub additionally needs a 32-byte Key-Encryption-Key (base64/hex/raw):
kubectl -n restorna-prod create secret generic connectorhub-secrets \
  --from-literal=DATABASE_URL=postgres://… \
  --from-literal=NATS_URL=nats://nats:4222 \
  --from-literal=JWT_PUBLIC_KEY=… \
  --from-literal=CONNECTOR_KEK="$(openssl rand -base64 32)"
```

---

## 5. AWS

### EKS (managed Kubernetes)
Use the Helm umbrella or Kustomize overlays unchanged. Cloud specifics live in an
overlay, **not** the base:
- Ingress: AWS Load Balancer Controller — add `alb.ingress.kubernetes.io/*`
  annotations + `ingressClassName: alb` in the overlay/umbrella `ingress.annotations`.
- Postgres: **RDS/Aurora** → set `postgresql.enabled=false`, put the RDS DSN in
  `DATABASE_URL`.
- NATS: in-cluster NATS subchart, or NATS on its own nodes.
- Redis: **ElastiCache** → `redis.enabled=false`.
- Secrets: External Secrets Operator backed by AWS Secrets Manager.
- Images: push to **ECR** (retag `ghcr.io/restorna/*` → `<acct>.dkr.ecr…`).

### ECS Fargate (serverless containers, no K8s)
- One **Task Definition per service** using the same image; set the container
  port to `8080` and `PORT=8080` (or any value — the app honors `$PORT`).
- One **ECS Service** per task with an **ALB target group**; health check path
  `/healthz`.
- Postgres = RDS; NATS = a small ECS service (`nats:2-alpine -js`) reachable via
  Cloud Map service discovery (`nats.local`); Redis = ElastiCache.
- Secrets from Secrets Manager mapped to container env (`secrets:` in the task def).
- Autoscaling: Application Auto Scaling on CPU (mirrors the HPA).

---

## 6. GCP

### GKE
Same Helm/Kustomize. Overlay specifics:
- Ingress: GCE ingress or nginx; managed certs via `networking.gke.io/managed-certificates`.
- Postgres: **Cloud SQL** (`postgresql.enabled=false`, DSN via the Cloud SQL Auth
  Proxy sidecar or private IP).
- Redis: **Memorystore** (`redis.enabled=false`); NATS in-cluster.
- Secrets: External Secrets Operator + Secret Manager, or Workload Identity.
- Images: **Artifact Registry**.

### Cloud Run (serverless containers, no K8s)
- Deploy each service image as its own Cloud Run service; Cloud Run sets `$PORT`
  (the app already binds it). Health: container must answer on `$PORT` — `/healthz`
  works as a startup/liveness probe.
- Postgres = Cloud SQL (connector or private VPC). Redis = Memorystore over a
  Serverless VPC connector.
- NATS: Cloud Run is request-scoped, so run **NATS on GKE or a GCE VM** and reach
  it over the VPC connector (don't run a broker as a Cloud Run service).
- Inter-service URLs = the Cloud Run HTTPS URLs (set `*_URL` env vars).
- Secrets: Secret Manager mapped to env.

---

## 7. Azure

### AKS
Same Helm/Kustomize. Overlay specifics:
- Ingress: AGIC (App Gateway) or nginx; certs via cert-manager.
- Postgres: **Azure Database for PostgreSQL Flexible Server** (`postgresql.enabled=false`).
- Redis: **Azure Cache for Redis** (`redis.enabled=false`); NATS in-cluster.
- Secrets: Azure Key Vault via the Secrets Store CSI driver / External Secrets.
- Images: **ACR**.

(Azure Container Apps is the serverless-container analogue to Cloud Run/ECS and
works the same way: one app per image, `$PORT` honored, Postgres/Redis managed,
NATS on AKS or a VM.)

---

## 8. Portability checklist

- [x] One image per service, built from repo root, distroless + non-root.
- [x] All configuration via env vars; service binds `$PORT`.
- [x] No cloud SDK / no cloud-specific primitive in app code.
- [x] Infra (Postgres/NATS/Redis) is swappable: in-cluster subchart **or** managed.
- [x] Cloud-specific bits (ingress annotations, secret backends, registries) live
      in **overlays**, never in the base chart/manifests.
- [x] Health on `/healthz` + `/readyz`; HPA/autoscaling on CPU.
- [x] Migrations run on startup (embedded goose) — no platform-specific job needed.

No lock-in: switching clouds means changing the registry, the managed-service
endpoints, and one overlay of annotations. The images and the app never change.
