# FIRST_BUILD — static compile-readiness notes

Static (read-only, no Go toolchain) audit of the monorepo's services against the
proto contracts in `proto/restorna/*/v1/`, the shared surface in
`pkg/INTERFACES.md`, and `CONVENTIONS.md`. Only clear, compiler-certain errors
were fixed; anything requiring a judgement call or a real compiler is recorded as
a "verify on first compile" item rather than guessed at.

## M1 (control plane)

Scope: `services/{identity, tenant, entitlements, staff, settings, onboarding, gateway}`.

### Summary
- **Total fixes applied: 1**
- Import paths (`gen/go/restorna/<ctx>/v1` + `.../<ctx>v1connect`, and `pkg/*`):
  all seven services resolve correctly against the proto package/dir layout and
  the `buf.gen.yaml` managed `go_package_prefix`. No mismatches.
- Connect constructors: every handler/client uses
  `New<Svc>ServiceHandler` / `New<Svc>ServiceClient` (Identity, Tenant,
  Entitlements, Staff, Settings, Onboarding). Correct.
- Generated getters: all `.GetX()` calls use protobuf-Go casing
  (`GetOwnerId`, `GetUrl`, `GetGstin`, `GetChallengeId`, `GetUserId`,
  `GetRestaurantId`, `GetType`, `GetDefault`, ...). No `GetOwnerID`/`GetURL`
  style bugs found.
- Enum constants: all match the proto
  (`commonv1.Role_ROLE_*`, `identityv1.Realm_REALM_*`,
  `identityv1.Channel_CHANNEL_*`, `settingsv1.ValueType_*`,
  `settingsv1.Scope_SCOPE_*`, `onboardingv1.Step_STEP_*`). Correct.
- `pkg` signature usage matches `INTERFACES.md`: `config.Load[T]`,
  `pg.Open/WithTenant/Migrate`, `grpcx.NewServer/Mount/Run` + the four
  interceptors, `outbox.Stage(tx,e)` / `outbox.Relay(ctx,pool,bus)`,
  `events.New(typ,tenant,data)`, `eventbus/nats.Connect(url)`,
  `auth.Sign/Verify`, `errors.ToConnect`, `tenancy.From/With/Require`. Correct
  arg counts/names everywhere.
- go.mod: every module path is correct, and each has the two sibling
  `replace` directives (`=> ../../pkg`, `=> ../../gen/go`) plus the direct
  requires used. Left version alignment untouched (already standardized).

### Per service

**identity** — 1 fix.
- FIXED: `services/identity/internal/adapters/pg/repo.go` referenced
  `time.Time{}` (in the `var _ = time.Time{}` "keep-referenced" line) but did
  not import `"time"` → `undefined: time`. Added `"time"` to the import block.
  The existing reference now satisfies the import, so it is not left unused.
- Everything else (handler, signer adapter, app, domain, ports, main.go
  wiring, migrations embed) is consistent.

**tenant** — no fixes.
- grpc handler embeds `UnimplementedTenantServiceHandler`, asserts the
  interface, maps every RPC, and uses correct proto field names
  (`Gstin`, `Url`, `Id`). Entitlements client, pg repo (outbox staging),
  app/domain and main.go wiring all consistent.

**entitlements** — no fixes.
- grpc adapter, pg adapter (owner-scoped + empty-tenant paths), app/domain,
  and main.go all consistent. Plan/Entitlement map fields
  (`quotas`, `features`, `quota_overrides`, `feature_overrides`) used correctly.

**staff** — no fixes.
- grpc handler, entitlements client (quota reserve/release/check), invites
  sender, pg repo + role mapping, app (paging), domain, main.go all consistent.

**settings** — no fixes.
- grpc adapter (definition/value/scope mapping, `ownerFromCtx` falling back to
  request `TenantRef`), cache adapter, pg adapter (outbox staging), app/domain,
  main.go all consistent. `ValueType_*` / `Scope_*` enum use correct.

**onboarding** — no fixes.
- Saga app + domain state machine, pg repo (`encodeSteps`/`decodeSteps`),
  grpc handler, and all five downstream Connect clients
  (identity/tenant/entitlements/staff/settings) satisfy their ports and use
  correct constructors/getters. main.go wiring consistent.

**gateway** — no fixes.
- BFF handlers (`auth`, `me`, `owner`, `platform`, `manager`, `settings`,
  `roles`, `router`), the typed client `Set` (one generated `*ServiceClient`
  per backend), middleware (auth/cors/logging/ratelimit), and forward/clients
  wiring all consistent. Cross-file helpers
  (`writeJSON`, `writeErr`, `badRequest`, `fwd`, `onbStateJSON`,
  `*JSON` mappers, `scopeRef`) are all defined within package `bff`.

### Residual "verify on first compile" items
None specific to symbol/signature correctness were found beyond the fix above.
General items a real `go build` / `buf generate` will still confirm (could not be
statically guaranteed without the toolchain):

1. **`gen/go` must actually be generated.** All services import
   `github.com/restorna/platform/gen/go/...`; the repo only ships `gen/go/go.mod`.
   Run `buf generate` (via the Makefile) before building so the
   `restorna/<ctx>/v1` message packages and `<ctx>v1connect` handler/client
   packages exist. The import paths and expected symbol names are correct; they
   just need the generated code present.
2. **Version alignment across go.mod files** was intentionally left as-is per
   instructions. `go build` will surface any residual mismatch; `go work sync`
   at the root is the intended reconciliation step.
3. **`Unimplemented<Svc>ServiceHandler` embeds** assume the connect-go
   `require_unimplemented_servers`-style forward-compat stub is emitted by the
   generator; that is the default for the buf connect-go plugin and matches
   usage, but is confirmed only once `gen/go` is generated.

## M3 (integration plane) + connector SDK

Static compile-readiness sweep over the M3 integration plane
(`services/{connectorhub, payments, notifications, aggregators}`) and the shared
connector SDK (`pkg/connector` + `pkg/connectors`). No Go toolchain / `buf` was
available — this pass is static only; `gen/go` is still empty (produced by
`make gen`), so all `restorna/<ctx>/v1` + `<ctx>v1connect` imports are
verify-on-compile. **7 surgical fixes** were made; the two systemic issues were a
connector-hub webhook-verifier interface mismatch and a payment event-subject
drift that broke the payment-webhook flow end to end.

### Fixes made

**`pkg/connectors` (payment event-subject drift → broke payments webhook flow):**
The four payment connectors emitted the wrong CloudEvent **type** (which becomes
the NATS subject). They published `restorna.payments.captured.v1` /
`.failed.v1`, but the shared constants (`connectors.EventPaymentCaptured/Failed`)
and the payments consumer both use the canonical
`restorna.connector.payment.captured` / `.failed`. So published payment webhooks
would never reach the payments consumer.
- `razorpay.go`, `paytm.go`, `phonepe.go` — replaced the hard-coded
  `"restorna.payments.*.v1"` strings in each `*Normalize`/`VerifyWebhook` with the
  `EventPaymentCaptured` / `EventPaymentFailed` constants (single vocabulary).
- `mockpay.go` — same constant fix, and it now echoes a `provider_ref` from the
  mock webhook body (`provider_ref`/`order_id`/`ref`) so the payments consumer can
  actually match a Payment (it drops events with an empty `provider_ref`). Uses the
  package-shared `firstNonEmpty` helper.

**`services/connectorhub` (webhook-verifier interface mismatch → broke aggregator
+ notification ingestion):** `internal/adapters/registry/registry.go` recognized
only the **payment-style** `VerifyWebhook(ctx, body, sig string)` shape. Aggregator
and notification connectors implement the **headers-style**
`connector.WebhookVerifier` = `VerifyWebhook(ctx, body, headers map[string]string)`
instead, so the `c.(WebhookVerifier)` type assertion failed for them and
`IngestWebhook` returned "connector does not accept webhooks" — the
`restorna.connector.aggregator.order.received` and delivery-status flows could
never verify.
- Renamed the local sig-string interface to `sigWebhookVerifier` and made
  `VerifyWebhook` try `connector.WebhookVerifier` (headers) **first**, then fall
  back to `sigWebhookVerifier` (extracted signature). Both branches return the same
  normalized `ports.Webhook`. Updated the package doc comment to match.

`payments`, `notifications`, `aggregators` service code required **no** fixes —
enum constants, generated-symbol usage, pkg usage, and event subjects were all
already correct (see verification below).

### Verified coherent (no change needed)

- **`pkg/connector`** declares `Connector`, `PaymentConnector`,
  `NotificationConnector`, `AggregatorConnector`, `WebhookVerifier`, `Manifest`,
  `Capability`, `Registry` — and the method signatures match how every service
  calls them (payment: `CreateIntent/Capture/Refund/VerifyWebhook`; notification
  `Send`; aggregator `PushMenu`; both non-payment caps carry the headers-based
  `WebhookVerifier`).
- **`pkg/connectors` coherence:** no duplicate top-level named identifiers across
  files (only legal repeated `var _` blank guards). `registry.go` `New`/`All`/`IDs`
  and the capability factories `NewPayment` (connectors.go), `NewNotification`
  (notifications.go) dispatch to **all 10** adapter constructors
  (razorpay/paytm/phonepe/mockpay, twilio/msg91/lognotify, zomato/swiggy/mockagg).
  Every adapter has a `var _ connector.<Cap>Connector = (*T)(nil)` guard and a
  `Manifest()`.
- **Enum constants match proto** exactly: connector-hub
  `Capability_CAPABILITY_{PAYMENT,AGGREGATOR,CRM,ERP,NOTIFICATION,UNSPECIFIED}`;
  payments `Status_{CREATED,PENDING,CAPTURED,FAILED,REFUNDED,STATUS_UNSPECIFIED}`
  (proto uses bare value names → `Status_CREATED` etc.); notifications
  `Channel_{SMS,WHATSAPP,EMAIL,PUSH,CHANNEL_UNSPECIFIED}` and
  `DeliveryStatus_{QUEUED,SENT,DELIVERED,FAILED,DELIVERY_UNSPECIFIED}`.
- **Generated import + symbol names:** all four services import
  `restorna/<ctx>/v1` + the connect package named after the proto **file** segment
  (`connectorv1connect`, `paymentsv1connect`, `notificationsv1connect`,
  `aggregatorsv1connect`, plus cross-context `catalogv1connect`,
  `orderingv1connect`, `entitlementsv1connect`). connector-hub is
  `ConnectorHubService` under package `restorna.connector.v1` →
  `connectorv1connect.New{Handler,Client}` — used correctly by connector-hub,
  payments, notifications, and aggregators. Message getters (`GetConnectorId`,
  `GetConfig`, `GetMinor`, `GetItems`, nested `ExternalOrder_Item`,
  `PlaceOrderRequest_NewLine`, catalog Item `GetCategoryId/GetVeg/GetStation`) all
  match their protos.
- **`pkg` signature usage matches INTERFACES.md + actual pkg:** `config.Load[T]`,
  `pg.{Open,Migrate}` (Migrate takes `embed.FS`), `outbox.{Relay,Stage,Bus}`,
  `events.New(typ, tenantID, data)`, `eventbus/nats.{Connect,Subscribe}` (Connect
  returns `outbox.Bus`), `grpcx.{NewServer,Mount,Run,AuthInterceptor,
  LoggingInterceptor,OTelInterceptor,RecoverInterceptor}`,
  `tenancy.{From,With,ErrPermissionDenied}`, `ids.{New,Valid}`, `money.New`,
  `commonv1.Role_ROLE_{CASHIER,MANAGER}`. The connector-hub entitlements client
  matches the entitlements proto (`ReserveQuota/ReleaseQuota/CheckQuota/HasFeature`
  + all request/response fields).
- **Event subjects align on both sides** (post-fix):
  - Payments: producer `pkg/connectors` (now) `restorna.connector.payment.captured`
    / `.failed` = consumer `payments/.../nats.SubjectPaymentCaptured/Failed`. ✅
  - Aggregators: producer `connectors.EventAggregatorOrderReceived` =
    `restorna.connector.aggregator.order.received` = consumer
    `aggregators/.../nats.SubjectOrderReceived`. ✅ (already matched)
  - Notifications: connectors emit `restorna.notifications.status.v1` (twilio/msg91)
    and `restorna.notifications.delivery.updated.v1` (lognotify); the consumer
    tolerantly subscribes to **both** (`DeliveryStatusEventTypes`), so they match.
    The two-type split is intentional but inconsistent — see residual note.

### Residual verify-on-compile items

- **`gen/go` is empty** — the single hard blocker. Run `make gen` (buf) before
  `go build`; until then every `restorna/<ctx>/v1` + `*v1connect` import is
  unresolved. All import paths and symbol names above are correct but only
  confirmable once codegen runs.
- **`Unimplemented<Svc>ServiceHandler` embeds** in all four grpc handlers assume
  the connect-go forward-compat stub is emitted (default for the buf connect-go
  plugin) — confirmed only after `gen/go` exists.
- **Cross-context symbols** used by aggregators (ordering `PlaceOrderRequest_NewLine`,
  catalog `Item`/`ListAllItems`) were verified against the ordering/catalog
  **protos**, not generated Go — confirm on compile.
- **`notifications/.../connectorhub.ErrNoProvider`** is a declared-but-currently-
  unreferenced package var. Legal in Go (unused package-level vars don't fail the
  compiler), so it is not a build blocker; flagging for a lint pass.

### Connector-interface / event-subject mismatches found (now fixed)

1. Payment connectors emitted `restorna.payments.*.v1` instead of the canonical
   `restorna.connector.payment.*` normalized subjects → **fixed** (4 files).
2. connector-hub registry recognized only the payment `VerifyWebhook(…, sig)`
   shape, silently rejecting all aggregator + notification webhooks (which use the
   `connector.WebhookVerifier` headers shape) → **fixed** (dual-shape dispatch).

### Minor notes (not fixed — out of surgical scope)

- **`CONNECTORHUB_URL` default drift:** payments/aggregators default to
  `http://connector-hub:8080`, notifications to `http://connectorhub:8080`. Runtime
  env, not a compile issue, but the service DNS name should be unified in deploy.
- **Empty stub files `pkg/connectors/{mock.go, providers.go}`** still contain only
  `package connectors` + an explanatory comment (superseded by `mockpay.go` and the
  per-provider HTTP adapters). Left in place as instructed; **they can be deleted**
  with no effect on the build.

**Fix count: 7** — `pkg/connectors/razorpay.go`, `paytm.go`, `phonepe.go`,
`mockpay.go` (payment subject constants; mockpay also gains `provider_ref`), and
`services/connectorhub/internal/adapters/registry/registry.go` (dual-shape webhook
verifier: interface rename + dispatch + doc-comment, counted as one fix).

## M2 (OMS data plane)

Static (read-only, no Go toolchain / no `buf`) audit of the seven OMS data-plane
services — `catalog, ordering, kitchen, floor, billing, promotions,
servicerequests` — against `proto/restorna/<ctx>/v1/*.proto`, `pkg/INTERFACES.md`,
and `CONVENTIONS.md`. Handler getters, generated ctors/nested types/enums, and all
cross-service Connect clients were checked against actual `.proto` field casing;
every `pkg/<x>` signature was checked against the real `pkg/*.go`.

**Fix count: 3** (all `go.mod` `require` additions).

### catalog
- **Verified clean.** `catalogv1`/`catalogv1connect` match the proto package;
  `NewCatalogServiceHandler`; all `Item` getters (`GetCategoryId`,
  `GetPrepMinutes`, `GetImage`, `GetStation`, `GetTags`…),
  `GetItemRequest.GetItemId`, `GetMenuRequest.GetPrefs/GetOnlyAvailable`,
  `SetOutletOverrideRequest` fields match `catalog.proto`. pkg usage correct. No
  cross-service clients / no consumers.
- **Fix**: `services/catalog/go.mod` — `main.go` imports `rs/zerolog/log` but the
  `require` was missing. Added `github.com/rs/zerolog v1.33.0`.

### ordering (event producer)
- **Verified clean.** `orderingv1`/`orderingv1connect`,
  `NewOrderingServiceHandler`; nested `PlaceOrderRequest.NewLine` consumed via
  `GetItems()` → `GetMenuItemId/GetName/GetQty/GetUnitPrice`; `Line`/`Order`
  mappers; `MarkBilled/Relocate/ListForTable` getters match `ordering.proto`.
  Outbox path correct: `tx.StageEvent` → `events.New` + `outbox.Stage`;
  `outbox.Relay` wired in `main.go`. Emits `restorna.ordering.order.placed.v1`
  (`app.EventOrderPlaced`) with payload `{order_id, restaurant_id, table_id,
  lines[]{line_id, menu_item_id, name, qty, station}}` — matches kitchen + floor
  consumer JSON tags.
- **Fix**: `services/ordering/go.mod` — `main.go` imports `rs/zerolog/log` but the
  `require` was missing (kitchen/floor already had it). Added
  `github.com/rs/zerolog v1.33.0`.

### kitchen (consumer of ordering; producer; catalog client)
- **Verified clean, no fixes.** Consumer subject
  `SubjectOrderPlaced = "restorna.ordering.order.placed.v1"` — EXACT match to
  ordering's emit (durable `kitchen-order-placed`). Emits
  `restorna.kitchen.ticket.ready.v1` (`EventTicketReady`) and
  `restorna.kitchen.ticket.served.v1` (`EventTicketServed`). Catalog client
  imports `catalogv1connect`/`NewCatalogServiceClient`, calls
  `GetItem(GetItemRequest{ItemId})` → `GetName()/GetStation()`. Handler getters
  (`GetTicketId`, `GetItemIndex`, nested `ReceiveTicketRequest.I` →
  `GetName/GetStation`) match `kitchen.proto`.

### floor (consumer of ordering + kitchen; clients: kitchen, billing, settings, ordering)
- **Verified clean, no fixes.** Two consumer subjects, both EXACT matches:
  `restorna.ordering.order.placed.v1` (durable `floor-order-placed`) and
  `restorna.kitchen.ticket.served.v1` (durable `floor-ticket-served`).
  Cross-service clients import the right generated client packages:
  `kitchenv1connect.NewKitchenServiceClient` (GetBoard/ServeQueue →
  `GetTickets/GetTable`), `billingv1connect.NewBillingServiceClient` (ListOpen →
  `GetBills/GetPaid/GetTable`), `orderingv1connect.NewOrderingServiceClient`
  (Relocate → `RelocateRequest{FromTable,ToTable}`/`GetMoved`),
  `settingsv1connect.NewSettingsServiceClient`
  (`GetEffective(GetEffectiveRequest{Scope *commonv1.TenantRef, Keys, Namespace})`
  → `GetValues()` → `SettingValue.GetKey()/GetValue().GetRaw()`) — all match the
  protos (settings `GetEffectiveRequest{scope,keys,namespace}` confirmed). Handler
  getters (`GetSrc/GetDst/GetN/GetWaiterId/GetTables/GetType`) and
  `MoveResponse.Verb` match `floor.proto`. `config.Load[Config]` embeds
  `config.Base`, passes `cfg.Base` to `grpcx.NewServer`.

### billing (consumer of ordering + own events; clients: ordering, catalog, promotions, settings)
- **Verified clean, no fixes.** Consumer
  `restorna.ordering.order.placed.v1` — EXACT match. Self-emitted
  `bill.opened`/`bill.finalized` consumer subjects equal the producer consts. A
  `bill`-gated `restorna.servicerequests.raised.v1` consumer is internally
  consistent (producer outside the M2 ordering/kitchen emit set). Cross-service
  clients correct: ordering (`ListForTable/MarkBilled`), promotions (`Evaluate` →
  `GetDiscount/GetApplied`), settings (`GetEffective`), catalog. **The potential
  `catalog Item.GetCategory()` trap is ABSENT** — the client uses
  `item.GetCategoryId()` and resolves the human category name via
  `ListCategories()` → `GetCategories()` → `cat.GetId()/GetName()` (all real proto
  symbols). `GetBillId/GetOrderIds` and Bill/Section/Tab/Payment getters match
  `billing.proto`.

### promotions
- **Verified clean** apart from the go.mod require. `promotionsv1connect`,
  `NewPromotionsServiceHandler`; `Coupon` getters (`GetMinOrder/GetStartsAt/
  GetEndsAt`, `value` int64, `starts_at`/`ends_at` string), `EvaluateRequest.
  GetSubtotal/GetCouponCode`, `EvaluateResponse{discount,applied}` match
  `promotions.proto`. No cross-service clients / no consumers.
- **Fix**: `services/promotions/go.mod` — `main.go` imports `rs/zerolog/log`;
  `require` was missing. Added `github.com/rs/zerolog v1.33.0`.

### servicerequests
- **Verified clean, no fixes.** `servicerequestsv1connect`,
  `NewServiceRequestsServiceHandler`; `Request` getters (`table` int32,
  `created_at` int64 via `.UnixMilli()`), `AcknowledgeRequest.GetRequestId`,
  `EscalateDueRequest.GetNow`, `EscalateDueResponse.Escalated` match
  `servicerequests.proto`. go.mod already had `rs/zerolog`.
- **Note (not a defect):** it has a `SettingsService` client
  (`internal/adapters/settings/client.go`) reading `floor.call.*`
  cooldown/escalate keys via `GetEffective`; usage matches `settings.proto` and
  degrades to defaults on error.

### Event subject mismatches found
- **None.** Every OMS consumer subject constant matches the producer emit string
  exactly: ordering `restorna.ordering.order.placed.v1` → kitchen, floor, billing
  (identical constants); kitchen `restorna.kitchen.ticket.ready.v1` /
  `…ticket.served.v1` → floor consumes `…ticket.served.v1`.
  `pkg/eventbus/nats.Subscribe` uses the event type string directly as the NATS
  subject (`subjectFor` is identity), so const == emit string is the correct check.

### Residual "verify on first compile" items (M2)
1. **`gen/go` is not generated** (repo ships only `gen/go/go.mod`). All seven
   services import `.../gen/go/restorna/<ctx>/v1` + `<ctx>v1connect`. Every
   referenced generated symbol was validated against `.proto` source and is
   deterministic under standard `protoc-gen-go`/`connect-go` output, but final
   confirmation needs `buf generate` (Makefile) before `go build`.
2. **go.mod transitive requires** (`jackc/pgconn` in `adapters/pg/errors.go`, nats
   via `pkg`) are intentionally left for `go mod tidy`; `go work sync` at root
   reconciles versions. Not defects.
3. **`Unimplemented<Svc>ServiceHandler` forward-compat embeds** in each grpc
   handler assume the buf connect-go plugin emits them (its default) — confirmed
   only once `gen/go` exists.
