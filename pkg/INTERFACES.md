# Shared `pkg/` interfaces — the fixed surface every service builds against

Module path root: `github.com/restorna/platform`. These signatures are CONTRACTS:
the `pkg` foundation agent implements them; service agents consume them. Do not
diverge. (Generated proto/Connect code lives in `gen/go/...`.)

## pkg/ids
```go
func New(prefix string) string   // ULID, e.g. New("out") -> "out_01HX..."
func Valid(prefix, id string) bool
```

## pkg/money
```go
type Money struct { Minor int64; Currency string }
func New(minor int64, ccy string) Money
func (m Money) Add(o Money) (Money, error)
func (m Money) Pct(p float64) Money
func (m Money) String() string   // "₹240.00"
```

## pkg/tenancy
```go
type Scope struct { OwnerID, BrandID, RestaurantID string; Role commonv1.Role; UserID string }
func From(ctx context.Context) (Scope, bool)
func With(ctx context.Context, s Scope) context.Context
func (s Scope) Require(role ...commonv1.Role) error   // PermissionDenied if not allowed
```

## pkg/errors  (map domain errors to Connect codes in the grpc adapter only)
```go
var ErrNotFound      = errors.New("not found")
var ErrAlreadyExists = errors.New("already exists")
var ErrQuotaExceeded = errors.New("quota exceeded")
var ErrInvalid       = errors.New("invalid argument")
func ToConnect(err error) *connect.Error          // maps the above to codes
func Field(err error, field, msg string) error    // attaches validation detail
```

## pkg/config  (12-factor; works on Render, ECS, K8s — just env)
```go
type Base struct {
  Port        string `env:"PORT" default:"8080"`
  DatabaseURL string `env:"DATABASE_URL"`
  NatsURL     string `env:"NATS_URL"`
  JWTPubKey   string `env:"JWT_PUBLIC_KEY"`
  OTLPEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`
  Env         string `env:"APP_ENV" default:"dev"`
}
func Load[T any]() (T, error)   // reflect over `env`/`default` tags
```

## pkg/pg  (Postgres via pgx; RLS-aware)
```go
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error)
// WithTenant runs fn in a tx with app.tenant_id set so RLS scopes rows.
func WithTenant(ctx context.Context, pool *pgxpool.Pool, tenantID string,
    fn func(pgx.Tx) error) error
func Migrate(dsn string, fs embed.FS) error   // goose embedded migrations
```

## pkg/events  (CloudEvents envelope)
```go
type Event struct { ID, Type, TenantID, Source string; OccurredAt time.Time; Data json.RawMessage }
func New(typ, tenantID string, data any) Event
```

## pkg/outbox  (transactional outbox + relay)
```go
// Stage writes an event row in the SAME tx as the business change.
func Stage(tx pgx.Tx, e events.Event) error
// Relay drains unpublished rows to the bus (run as a goroutine / sidecar).
func Relay(ctx context.Context, pool *pgxpool.Pool, bus Bus) error
type Bus interface { Publish(ctx context.Context, e events.Event) error }
```

## pkg/eventbus/nats
```go
func Connect(url string) (Bus, error)                 // implements outbox.Bus
func Subscribe(ctx, url, subject, durable string, h func(events.Event) error) error // idempotent consumer (dedupe on Event.ID)
```

## pkg/grpcx  (Connect server + interceptors)
```go
func NewServer(base config.Base) *Server          // wraps net/http h2c
func (s *Server) Mount(pattern string, h http.Handler)  // mount a Connect handler
func (s *Server) Run(ctx context.Context) error   // graceful, /healthz /readyz
// Interceptors:
func AuthInterceptor(pubKey string) connect.Interceptor   // verify JWT -> tenancy.Scope in ctx
func LoggingInterceptor() connect.Interceptor
func OTelInterceptor() connect.Interceptor
func RecoverInterceptor() connect.Interceptor
```

## pkg/auth  (JWT)
```go
type Claims struct { UserID string; Role commonv1.Role; Owner, Brand, Restaurant string; jwt.RegisteredClaims }
func Sign(priv ed25519.PrivateKey, c Claims, ttl time.Duration) (string, error)
func Verify(pub ed25519.PublicKey, token string) (Claims, error)
```

## pkg/connector  (the plug-and-play SDK — used by integration plane in M3)
```go
type Capability string // "payment" | "aggregator" | "crm" | "erp" | "notification"
type Manifest struct { ID, Name string; Capabilities []Capability; ConfigSchema json.RawMessage }
type Connector interface {
  Manifest() Manifest
  Init(ctx context.Context, cfg map[string]string) error
}
type PaymentConnector interface {
  Connector
  CreateIntent(ctx context.Context, amount money.Money, ref string) (provRef string, err error)
  Capture(ctx context.Context, provRef string) (receipt json.RawMessage, err error)
  Refund(ctx context.Context, provRef string, amount money.Money) error
  VerifyWebhook(ctx context.Context, body []byte, sig string) (events.Event, error)
}
type Registry interface { Register(Connector); Get(id string) (Connector, bool); ByCapability(Capability) []Connector }
```

## Service `main.go` skeleton (every service)
```go
func main() {
  cfg, _ := config.Load[config.Base]()
  pool, _ := pg.Open(ctx, cfg.DatabaseURL); pg.Migrate(cfg.DatabaseURL, migrationsFS)
  bus, _ := nats.Connect(cfg.NatsURL)
  repo := pgadapter.New(pool); uc := app.New(repo, bus, clients...)
  srv := grpcx.NewServer(cfg)
  path, handler := xxxv1connect.NewXxxServiceHandler(grpcadapter.New(uc),
      connect.WithInterceptors(grpcx.AuthInterceptor(cfg.JWTPubKey), grpcx.LoggingInterceptor(), grpcx.OTelInterceptor(), grpcx.RecoverInterceptor()))
  srv.Mount(path, handler)
  go outbox.Relay(ctx, pool, bus)
  srv.Run(ctx)
}
```
