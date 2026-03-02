// Package shop is the demo store the example agents work on: customers,
// orders, support tickets, a catalogue and inventory, with a GraphQL API in
// front. It stands in for the internal systems a real agent would be
// connected to, and it can generate scenarios whose right answers are known.
package shop

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("not found")

// Shop reads and writes the demo store's tables.
type Shop struct {
	Pool *pgxpool.Pool
}

// Customer is a shopper.
type Customer struct {
	ID          string    `db:"id"`
	Email       string    `db:"email"`
	Name        string    `db:"name"`
	Country     string    `db:"country"`
	OrdersCount int       `db:"orders_count"`
	Chargebacks int       `db:"chargebacks"`
	CreatedAt   time.Time `db:"created_at"`
}

// AccountAgeDays is how long ago the customer signed up, in whole days.
func (c *Customer) AccountAgeDays() int {
	return int(time.Since(c.CreatedAt).Hours() / 24)
}

// Product is a catalogue entry. Category, colour and material may be empty
// until someone, or some agent, fills them in.
type Product struct {
	SKU         string    `db:"sku"`
	Title       string    `db:"title"`
	Description string    `db:"description"`
	Category    string    `db:"category"`
	Color       string    `db:"color"`
	Material    string    `db:"material"`
	PriceCents  int       `db:"price_cents"`
	CreatedAt   time.Time `db:"created_at"`
}

// Inventory is the stock position of one SKU.
type Inventory struct {
	SKU           string  `db:"sku"`
	OnHand        int     `db:"on_hand"`
	OnOrder       int     `db:"on_order"`
	DailySales    float64 `db:"daily_sales"`
	LeadTimeDays  int     `db:"lead_time_days"`
	CoverDays     int     `db:"cover_days"`
	PackSize      int     `db:"pack_size"`
	UnitCostCents int     `db:"unit_cost_cents"`
}

// ReorderQuantity applies the shop's replenishment policy and returns how
// many units to order, or zero when stock is sufficient.
//
// Stock is sufficient while on-hand plus on-order exceeds what will sell
// during the supplier's lead time. Otherwise the shop orders enough to cover
// the lead time plus its target days of cover, rounded up to whole packs.
func (i *Inventory) ReorderQuantity() int {
	available := float64(i.OnHand + i.OnOrder)
	if available > i.DailySales*float64(i.LeadTimeDays) {
		return 0
	}
	need := i.DailySales*float64(i.LeadTimeDays+i.CoverDays) - available
	packs := math.Ceil(need / float64(i.PackSize))
	return int(packs) * i.PackSize
}

// Order is a customer order.
type Order struct {
	ID              string    `db:"id"`
	CustomerID      string    `db:"customer_id"`
	Status          string    `db:"status"`
	Items           string    `db:"items"`
	TotalCents      int       `db:"total_cents"`
	BillingCountry  string    `db:"billing_country"`
	ShippingCountry string    `db:"shipping_country"`
	IPCountry       string    `db:"ip_country"`
	PaymentAttempts int       `db:"payment_attempts"`
	Carrier         string    `db:"carrier"`
	Tracking        string    `db:"tracking"`
	ETA             string    `db:"eta"`
	HoldReason      string    `db:"hold_reason"`
	PlacedAt        time.Time `db:"placed_at"`
}

// Ticket is a customer support request.
type Ticket struct {
	ID               string    `db:"id"`
	CustomerID       string    `db:"customer_id"`
	OrderID          *string   `db:"order_id"`
	Subject          string    `db:"subject"`
	Body             string    `db:"body"`
	Status           string    `db:"status"`
	Reply            string    `db:"reply"`
	EscalationReason string    `db:"escalation_reason"`
	CreatedAt        time.Time `db:"created_at"`
}

// PurchaseOrder is a replenishment order placed with a supplier.
type PurchaseOrder struct {
	ID        string    `db:"id"`
	SKU       string    `db:"sku"`
	Quantity  int       `db:"quantity"`
	Note      string    `db:"note"`
	CreatedAt time.Time `db:"created_at"`
}

// DailySummary is the last 24 hours at a glance.
type DailySummary struct {
	OrdersPlaced     int `db:"orders_placed"`
	RevenueCents     int `db:"revenue_cents"`
	OpenTickets      int `db:"open_tickets"`
	EscalatedTickets int `db:"escalated_tickets"`
	OrdersOnHold     int `db:"orders_on_hold"`
	SkusToReorder    int `db:"skus_to_reorder"`
}

func one[T any](ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) (*T, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	row, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[T])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

const (
	customerColumns  = `id, email, name, country, orders_count, chargebacks, created_at`
	productColumns   = `sku, title, description, category, color, material, price_cents, created_at`
	inventoryColumns = `sku, on_hand, on_order, daily_sales, lead_time_days, cover_days, pack_size, unit_cost_cents`
	orderColumns     = `id, customer_id, status, items, total_cents, billing_country, shipping_country, ip_country,
		payment_attempts, carrier, tracking, eta, hold_reason, placed_at`
	ticketColumns = `id, customer_id, order_id, subject, body, status, reply, escalation_reason, created_at`
)

// Customer looks a customer up by ID.
func (s *Shop) Customer(ctx context.Context, id string) (*Customer, error) {
	return one[Customer](ctx, s.Pool, `select `+customerColumns+` from shop.customers where id = $1`, id)
}

// Product looks a product up by SKU.
func (s *Shop) Product(ctx context.Context, sku string) (*Product, error) {
	return one[Product](ctx, s.Pool, `select `+productColumns+` from shop.products where sku = $1`, sku)
}

// Inventory looks a stock position up by SKU.
func (s *Shop) Inventory(ctx context.Context, sku string) (*Inventory, error) {
	return one[Inventory](ctx, s.Pool, `select `+inventoryColumns+` from shop.inventory where sku = $1`, sku)
}

// Order looks an order up by ID.
func (s *Shop) Order(ctx context.Context, id string) (*Order, error) {
	return one[Order](ctx, s.Pool, `select `+orderColumns+` from shop.orders where id = $1`, id)
}

// Ticket looks a ticket up by ID.
func (s *Shop) Ticket(ctx context.Context, id string) (*Ticket, error) {
	return one[Ticket](ctx, s.Pool, `select `+ticketColumns+` from shop.tickets where id = $1`, id)
}

// ReplyToTicket answers an open ticket.
func (s *Shop) ReplyToTicket(ctx context.Context, id, body string) (*Ticket, error) {
	return s.closeTicket(ctx, id, `update shop.tickets set status = 'answered', reply = $2
		where id = $1 and status = 'open' returning `+ticketColumns, body)
}

// EscalateTicket hands an open ticket to a person.
func (s *Shop) EscalateTicket(ctx context.Context, id, reason string) (*Ticket, error) {
	return s.closeTicket(ctx, id, `update shop.tickets set status = 'escalated', escalation_reason = $2
		where id = $1 and status = 'open' returning `+ticketColumns, reason)
}

func (s *Shop) closeTicket(ctx context.Context, id, sql, text string) (*Ticket, error) {
	if text == "" {
		return nil, errors.New("text must not be empty")
	}
	t, err := one[Ticket](ctx, s.Pool, sql, id, text)
	if errors.Is(err, ErrNotFound) {
		if existing, lookupErr := s.Ticket(ctx, id); lookupErr == nil {
			return nil, fmt.Errorf("ticket %s is already %s", id, existing.Status)
		}
	}
	return t, err
}

// ProductUpdate holds the catalogue fields to change. Nil fields are left alone.
type ProductUpdate struct {
	Category, Color, Material, Description *string
}

// UpdateProduct changes a product's catalogue fields.
func (s *Shop) UpdateProduct(ctx context.Context, sku string, u ProductUpdate) (*Product, error) {
	return one[Product](ctx, s.Pool, `
		update shop.products set
			category = coalesce($2, category), color = coalesce($3, color),
			material = coalesce($4, material), description = coalesce($5, description)
		where sku = $1 returning `+productColumns, sku, u.Category, u.Color, u.Material, u.Description)
}

// CreatePurchaseOrder orders stock from a supplier and counts it as on order.
func (s *Shop) CreatePurchaseOrder(ctx context.Context, sku string, quantity int, note string) (*PurchaseOrder, error) {
	if quantity <= 0 {
		return nil, errors.New("quantity must be positive")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `update shop.inventory set on_order = on_order + $2 where sku = $1`, sku, quantity)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	rows, err := tx.Query(ctx, `
		insert into shop.purchase_orders (id, sku, quantity, note)
		values ('PO-' || nextval('shop.po_seq'), $1, $2, $3)
		returning id, sku, quantity, note, created_at`, sku, quantity, note)
	if err != nil {
		return nil, err
	}
	po, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[PurchaseOrder])
	if err != nil {
		return nil, err
	}
	return &po, tx.Commit(ctx)
}

// HoldOrder stops an order from shipping until someone reviews it.
func (s *Shop) HoldOrder(ctx context.Context, id, reason string) (*Order, error) {
	if reason == "" {
		return nil, errors.New("reason must not be empty")
	}
	o, err := one[Order](ctx, s.Pool, `
		update shop.orders set status = 'on_hold', hold_reason = $2
		where id = $1 and status = 'processing' returning `+orderColumns, id, reason)
	if errors.Is(err, ErrNotFound) {
		if existing, lookupErr := s.Order(ctx, id); lookupErr == nil {
			return nil, fmt.Errorf("order %s is %s and cannot be put on hold", id, existing.Status)
		}
	}
	return o, err
}

// Summary reports the last 24 hours.
func (s *Shop) Summary(ctx context.Context) (*DailySummary, error) {
	return one[DailySummary](ctx, s.Pool, `
		select
			(select count(*) from shop.orders where placed_at > now() - interval '24 hours')::int as orders_placed,
			(select coalesce(sum(total_cents), 0) from shop.orders where placed_at > now() - interval '24 hours')::int as revenue_cents,
			(select count(*) from shop.tickets where status = 'open')::int as open_tickets,
			(select count(*) from shop.tickets where status = 'escalated' and created_at > now() - interval '24 hours')::int as escalated_tickets,
			(select count(*) from shop.orders where status = 'on_hold')::int as orders_on_hold,
			(select count(*) from shop.inventory where on_hand + on_order <= daily_sales * lead_time_days)::int as skus_to_reorder`)
}
