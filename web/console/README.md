# Restorna Console

The **admin web console** for the Restorna control plane — a React + Vite +
TypeScript SPA that sits on top of the gateway's JSON BFFs
(`services/gateway/internal/bff`). The console never speaks gRPC; every screen calls a
plain `/api/*` JSON route and carries the bearer token from OTP login.

## Quick start

```bash
cd web/console
npm install
npm run dev            # http://localhost:5173
```

Point it at a gateway with `VITE_GATEWAY_URL` (default `http://localhost:8080`):

```bash
VITE_GATEWAY_URL=http://localhost:8080 npm run dev
```

In dev, Vite proxies `/api` → the gateway (see `vite.config.ts`), so the browser stays
same-origin and you never hit CORS. In a **built** app the base URL is inlined at build
time and the app calls the gateway directly — the gateway's `CORS_ALLOWED_ORIGINS` must
then include the console's origin.

> **Dev OTP code:** when the identity service runs with `APP_ENV=dev`, the one-time
> code is **`123456`** (surfaced as a hint on the login screen).

### Scripts

| Command | Does |
|---|---|
| `npm run dev` | Vite dev server + `/api` proxy |
| `npm run build` | Type-check (`tsc -b`) then `vite build` → `dist/` |
| `npm run preview` | Serve the built `dist/` locally |
| `npm run typecheck` | Types only, no emit |

## Screens

| Route | Screen | Role gate | Gateway routes used |
|---|---|---|---|
| `/login` | **OTP login** (email/phone → code, realm toggle, dev-code hint) | public | `POST /api/auth/start-otp`, `POST /api/auth/verify-otp`, `POST /api/auth/refresh` |
| `/onboarding` | **Owner onboarding wizard** — Account → Plan → Brand (logo + accent) → Outlet → Team → Go-live | owner / brand_admin | `POST /api/owner/onboarding/{start,submit-brand,submit-outlet,invite-team,complete}` |
| `/dashboard` | **Owner dashboard** — brands, outlets, plan usage meters, quick links | owner / platform_admin | `GET /api/owner/{brands,outlets,entitlement}` |
| `/team` | **Team** — staff list, add/disable/change-role/invite, seat quota | manager / owner | `GET/POST /api/manager/staff`, `/staff/disable`, `/staff/change-role`, `/staff/invite`; `GET /api/owner/entitlement` |
| `/settings` | **Settings** — business config grouped by namespace with definition metadata | manager / owner | `GET/POST /api/owner/settings` (owner) · `GET/POST /api/manager/settings` (manager) |
| `/platform` | **Platform admin** — owners lookup, plans editor (quotas + flags), connectors placeholder | platform_admin | `GET /api/platform/owner`, `GET /api/platform/entitlement`, `POST /api/platform/plan` |

Every authenticated screen is role-gated by the role returned from **`GET /api/me`**.

## How it maps to the gateway

- `src/lib/api.ts` — the single fetch wrapper: base URL + bearer + JSON + typed
  `ApiError` (401 drops the session, other codes surface to the UI).
- `src/lib/endpoints.ts` — one typed function per BFF route, mirroring
  `services/gateway/internal/bff/*.go` 1:1.
- `src/lib/types.ts` — JSON shapes exactly as the BFF `*JSON` helpers project them.
- `src/auth/SessionContext.tsx` — stores the token pair in `localStorage`,
  introspects via `/api/me`, and refreshes once on a stale access token.

### Known contract gaps (mirrored from the gateway README)

- **No `ListOwners` / `ListPlans` RPC** in M1 — the platform screen therefore does
  *lookup by owner id* and *upsert a plan by id*, not a full index.
- **No `ListDefinitions`** on the settings BFF — the settings editor carries the
  definition catalogue client-side in `src/lib/settingsDefs.ts` (shapes match
  `settings.proto` `Definition`). Swap for a fetch when the passthrough lands.

## Design language

Ports the luxe **ivory + brass** system from the demo app
(`Restorna/restorna/frontend/src/styles/tokens.css`) into `src/styles/tokens.css`, and
carries its UX psychology: restraint (one brass accent, exactly one primary action per
view — Hick), calm surfaces, one distinct status colour for live/warning states (Von
Restorff), sub-400ms feedback (Doherty), a Zeigarnik progress bar through onboarding,
and a celebratory go-live peak (peak-end). Skeletons, error/retry, toasts, and
`prefers-reduced-motion` are respected throughout; the layout is responsive.

## Deploy anywhere

**Docker (build → static nginx):**

```bash
docker build \
  --build-arg VITE_GATEWAY_URL=https://gateway.your-domain.app \
  -t restorna/console web/console
docker run -p 8080:8080 restorna/console
```

**Static host (Render / Netlify / Vercel / Cloudflare Pages / S3+CDN):**
build command `npm ci && npm run build`, publish dir `dist`, and an SPA rewrite of
`/*` → `/index.html`. `render.yaml` in this folder is a ready manifest; the same three
knobs port to any static host. Set `VITE_GATEWAY_URL` in the host's env and add the
console origin to the gateway's `CORS_ALLOWED_ORIGINS`.
