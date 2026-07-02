// Package pg is the Postgres implementation of ports.Repository using pgx. Every
// operation runs inside pkg/pg.WithTenant so app.tenant_id is set and RLS scopes
// rows to the restaurant. Outbox events + processed-event marks are staged in the
// same tx (pkg/outbox.Stage). Bill lines + payments are stored as JSONB columns on
// the bill row (billing reads/writes whole bills). The tabs read model is a
// separate table keyed by (restaurant_id, table) maintained by the consumers.
package pg

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"
	"github.com/restorna/platform/services/billing/internal/domain"
	"github.com/restorna/platform/services/billing/internal/ports"
)

// Repo implements ports.Repository over a pgx pool.
type Repo struct {
	pool *pgxpool.Pool
}

var _ ports.Repository = (*Repo)(nil)

// New builds a Repo from a connection pool.
func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// Atomic runs fn in a tenant-scoped transaction (RLS via app.tenant_id = restaurant).
func (r *Repo) Atomic(ctx context.Context, restaurantID string, fn func(ports.Tx) error) error {
	return pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		return fn(&txAdapter{tx: tx, restaurantID: restaurantID})
	})
}

// GetBill loads one bill by id (RLS-scoped).
func (r *Repo) GetBill(ctx context.Context, restaurantID, billID string) (domain.Bill, error) {
	var b domain.Bill
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		var serr error
		b, serr = scanBill(tx.QueryRow(ctx, selectBill+` WHERE id=$1`, billID))
		return serr
	})
	return b, err
}

// ListOpenBills returns the unpaid bills for the restaurant (the billing queue).
func (r *Repo) ListOpenBills(ctx context.Context, restaurantID string) ([]domain.Bill, error) {
	var out []domain.Bill
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, selectBill+` WHERE restaurant_id=$1 AND paid=false ORDER BY created_at, id`, restaurantID)
		if err != nil {
			return mapRead(err)
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBill(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return mapRead(rows.Err())
	})
	return out, err
}

// ListTabs returns the live billing board for the restaurant.
func (r *Repo) ListTabs(ctx context.Context, restaurantID string) ([]domain.Tab, error) {
	var out []domain.Tab
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, selectTab+` WHERE restaurant_id=$1 ORDER BY table_no`, restaurantID)
		if err != nil {
			return mapRead(err)
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTab(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return mapRead(rows.Err())
	})
	return out, err
}

// Seen reports whether eventID was already processed for the restaurant.
func (r *Repo) Seen(ctx context.Context, restaurantID, eventID string) (bool, error) {
	if eventID == "" {
		return false, nil
	}
	var seen bool
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM processed_events WHERE event_id=$1)`, eventID).Scan(&seen)
	})
	return seen, err
}

// txAdapter implements ports.Tx over a single pgx.Tx.
type txAdapter struct {
	tx           pgx.Tx
	restaurantID string
}

func (t *txAdapter) GetBill(ctx context.Context, billID string) (domain.Bill, error) {
	return scanBill(t.tx.QueryRow(ctx, selectBill+` WHERE id=$1`, billID))
}

func (t *txAdapter) InsertBill(ctx context.Context, b domain.Bill) error {
	linesJSON, err := json.Marshal(toLineRows(b.Lines))
	if err != nil {
		return err
	}
	paysJSON, err := json.Marshal(toPaymentRows(b.Payments))
	if err != nil {
		return err
	}
	orderIDs := b.OrderIDs
	if orderIDs == nil {
		orderIDs = []string{}
	}
	_, err = t.tx.Exec(ctx, `
		INSERT INTO bills (id, restaurant_id, table_label, order_ids, lines, discount_minor, payments, paid, currency, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		b.ID, t.restaurantID, b.Table, orderIDs, linesJSON, b.Discount.Minor, paysJSON, b.Paid, b.Currency, b.CreatedAt)
	return mapWrite(err)
}

func (t *txAdapter) UpdateBill(ctx context.Context, b domain.Bill) error {
	paysJSON, err := json.Marshal(toPaymentRows(b.Payments))
	if err != nil {
		return err
	}
	ct, err := t.tx.Exec(ctx, `
		UPDATE bills SET discount_minor=$2, payments=$3, paid=$4 WHERE id=$1`,
		b.ID, b.Discount.Minor, paysJSON, b.Paid)
	if err != nil {
		return mapWrite(err)
	}
	if ct.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (t *txAdapter) GetTab(ctx context.Context, table int32) (domain.Tab, bool, error) {
	tab, err := scanTab(t.tx.QueryRow(ctx, selectTab+` WHERE table_no=$1`, table))
	if err != nil {
		if err == domain.ErrNotFound {
			return domain.Tab{}, false, nil
		}
		return domain.Tab{}, false, err
	}
	return tab, true, nil
}

func (t *txAdapter) UpsertTab(ctx context.Context, tab domain.Tab) error {
	_, err := t.tx.Exec(ctx, `
		INSERT INTO tabs (restaurant_id, table_no, order_count, item_count, running_minor, asked, bill_id, bill_total_minor, currency)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (restaurant_id, table_no) DO UPDATE SET
			order_count=EXCLUDED.order_count,
			item_count=EXCLUDED.item_count,
			running_minor=EXCLUDED.running_minor,
			asked=EXCLUDED.asked,
			bill_id=EXCLUDED.bill_id,
			bill_total_minor=EXCLUDED.bill_total_minor,
			currency=EXCLUDED.currency`,
		t.restaurantID, tab.Table, tab.OrderCount, tab.ItemCount, tab.Running.Minor,
		tab.Asked, nullStr(tab.BillID), tab.BillTotal.Minor, tabCurrency(tab))
	return mapWrite(err)
}

func (t *txAdapter) DeleteTab(ctx context.Context, table int32) error {
	_, err := t.tx.Exec(ctx, `DELETE FROM tabs WHERE table_no=$1`, table)
	return mapWrite(err)
}

func (t *txAdapter) Seen(ctx context.Context, _ string, eventID string) (bool, error) {
	if eventID == "" {
		return false, nil
	}
	var seen bool
	err := t.tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM processed_events WHERE event_id=$1)`, eventID).Scan(&seen)
	return seen, err
}

func (t *txAdapter) StageEvent(ctx context.Context, eventType, restaurantID string, data any) error {
	e := events.New(eventType, restaurantID, data)
	e.Source = "billing"
	return outbox.Stage(t.tx, e)
}

func (t *txAdapter) MarkProcessed(ctx context.Context, restaurantID, eventID string) error {
	if eventID == "" {
		return nil
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO processed_events (event_id, restaurant_id, processed_at)
		VALUES ($1, $2, now())
		ON CONFLICT (event_id) DO NOTHING`,
		eventID, restaurantID)
	return mapWrite(err)
}

// --- scan helpers ---

const selectBill = `SELECT id, restaurant_id, table_label, order_ids, lines, discount_minor, payments, paid, currency, created_at FROM bills`
const selectTab = `SELECT restaurant_id, table_no, order_count, item_count, running_minor, asked, bill_id, bill_total_minor, currency FROM tabs`

// lineRow is the JSONB shape of a bill line (stable wire form).
type lineRow struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Category   string `json:"category"`
	PriceMinor int64  `json:"price_minor"`
}

func toLineRows(lines []domain.BillLine) []lineRow {
	out := make([]lineRow, 0, len(lines))
	for _, l := range lines {
		out = append(out, lineRow{ID: l.ID, Name: l.Name, Category: l.Category, PriceMinor: l.Price.Minor})
	}
	return out
}

// paymentRow is the JSONB shape of a payment.
type paymentRow struct {
	ID          string    `json:"id"`
	Method      string    `json:"method"`
	AmountMinor int64     `json:"amount_minor"`
	Ref         string    `json:"ref"`
	At          time.Time `json:"at"`
}

func toPaymentRows(pays []domain.Payment) []paymentRow {
	out := make([]paymentRow, 0, len(pays))
	for _, p := range pays {
		out = append(out, paymentRow{ID: p.ID, Method: p.Method, AmountMinor: p.Amount.Minor, Ref: p.Ref, At: p.At})
	}
	return out
}

type scanner interface {
	Scan(dest ...any) error
}

func scanBill(row scanner) (domain.Bill, error) {
	var (
		b        domain.Bill
		rid      string
		orderIDs []string
		linesRaw []byte
		discount int64
		paysRaw  []byte
		created  time.Time
	)
	if err := row.Scan(&b.ID, &rid, &b.Table, &orderIDs, &linesRaw, &discount, &paysRaw, &b.Paid, &b.Currency, &created); err != nil {
		return domain.Bill{}, mapRead(err)
	}
	b.RestaurantID = rid
	b.OrderIDs = orderIDs
	b.CreatedAt = created
	b.Discount = money.New(discount, b.Currency)

	var lrows []lineRow
	if len(linesRaw) > 0 {
		if err := json.Unmarshal(linesRaw, &lrows); err != nil {
			return domain.Bill{}, err
		}
	}
	lines := make([]domain.BillLine, 0, len(lrows))
	for _, lr := range lrows {
		lines = append(lines, domain.BillLine{
			ID: lr.ID, Name: lr.Name, Category: lr.Category, Price: money.New(lr.PriceMinor, b.Currency),
		})
	}
	b.Lines = lines

	var prows []paymentRow
	if len(paysRaw) > 0 {
		if err := json.Unmarshal(paysRaw, &prows); err != nil {
			return domain.Bill{}, err
		}
	}
	pays := make([]domain.Payment, 0, len(prows))
	for _, pr := range prows {
		pays = append(pays, domain.Payment{
			ID: pr.ID, Method: pr.Method, Amount: money.New(pr.AmountMinor, b.Currency), Ref: pr.Ref, At: pr.At,
		})
	}
	b.Payments = pays
	return b, nil
}

func scanTab(row scanner) (domain.Tab, error) {
	var (
		t           domain.Tab
		rid         string
		running     int64
		billID      *string
		billTotal   int64
		currency    string
	)
	if err := row.Scan(&rid, &t.Table, &t.OrderCount, &t.ItemCount, &running, &t.Asked, &billID, &billTotal, &currency); err != nil {
		return domain.Tab{}, mapRead(err)
	}
	t.RestaurantID = rid
	if currency == "" {
		currency = domain.DefaultCurrency
	}
	t.Running = money.New(running, currency)
	t.BillTotal = money.New(billTotal, currency)
	if billID != nil {
		t.BillID = *billID
	}
	return t, nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func tabCurrency(t domain.Tab) string {
	if t.Running.Currency != "" {
		return t.Running.Currency
	}
	if t.BillTotal.Currency != "" {
		return t.BillTotal.Currency
	}
	return domain.DefaultCurrency
}
