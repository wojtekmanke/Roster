package shop

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"
)

// Scenario is one piece of work for an agent, with the answer a correct
// agent would give. The generator knows the answer because it built the data.
type Scenario struct {
	// Event is the event to emit, such as "ticket.created".
	Event string
	// Payload is what the agent is told.
	Payload json.RawMessage
	// Expected is the ground truth. It goes to the graders, never to the agent.
	Expected json.RawMessage
}

// Counts says how many scenarios of each kind to generate.
type Counts struct {
	Tickets  int
	Products int
	Restocks int
	Orders   int
}

// Events emitted by the generator.
const (
	EventTicketCreated  = "ticket.created"
	EventProductCreated = "product.created"
	EventLowStock       = "inventory.low_stock"
	EventOrderPlaced    = "order.placed"
)

// Risk signals an order can carry. An order with two or more is fraudulent
// by the shop's policy.
const (
	SignalCountryMismatch = "country_mismatch"
	SignalPaymentRetries  = "payment_retries"
	SignalNewHighValue    = "new_account_high_value"
	SignalDisposableEmail = "disposable_email"
	SignalChargebacks     = "prior_chargebacks"
)

var disposableDomains = []string{"mailinator.com", "tempmail.dev", "10minutemail.com", "guerrillamail.com"}

// RiskSignals lists the fraud signals present on an order, per the policy in
// the fraud-screen agent's instructions.
func RiskSignals(o *Order, c *Customer) []string {
	var signals []string
	if o.BillingCountry != o.ShippingCountry || o.IPCountry != o.BillingCountry {
		signals = append(signals, SignalCountryMismatch)
	}
	if o.PaymentAttempts >= 3 {
		signals = append(signals, SignalPaymentRetries)
	}
	if time.Since(c.CreatedAt) < 48*time.Hour && o.TotalCents > 30000 {
		signals = append(signals, SignalNewHighValue)
	}
	domain := c.Email[strings.LastIndex(c.Email, "@")+1:]
	for _, d := range disposableDomains {
		if domain == d {
			signals = append(signals, SignalDisposableEmail)
		}
	}
	if c.Chargebacks >= 1 {
		signals = append(signals, SignalChargebacks)
	}
	return signals
}

// productType is a kind of product and what it can be made of.
type productType struct {
	noun      string
	category  string
	materials []string
}

var productTypes = []productType{
	{"Crew Tee", "apparel", []string{"cotton", "polyester"}},
	{"Hoodie", "apparel", []string{"cotton", "polyester"}},
	{"Beanie", "apparel", []string{"wool"}},
	{"Mug", "drinkware", []string{"ceramic"}},
	{"Tumbler", "drinkware", []string{"stainless steel"}},
	{"Water Bottle", "drinkware", []string{"stainless steel", "glass"}},
	{"Tote", "bags", []string{"canvas", "cotton"}},
	{"Backpack", "bags", []string{"polyester", "canvas"}},
	{"Notebook", "stationery", []string{"paper"}},
	{"Planner", "stationery", []string{"paper"}},
	{"Coaster Set", "home", []string{"bamboo", "ceramic"}},
	{"Throw Blanket", "home", []string{"wool", "cotton"}},
}

// Titles use trade words; the catalogue wants the plain value. Mapping one
// to the other is the enrichment agent's job.
var materialWords = map[string][]string{
	"cotton":          {"Cotton", "Organic Cotton"},
	"polyester":       {"Recycled Polyester", "Poly"},
	"wool":            {"Merino", "Wool"},
	"ceramic":         {"Stoneware", "Ceramic"},
	"stainless steel": {"Steel", "Stainless"},
	"glass":           {"Glass", "Borosilicate"},
	"canvas":          {"Canvas", "Waxed Canvas"},
	"paper":           {"Kraft", "Recycled Paper"},
	"bamboo":          {"Bamboo"},
}

var colorWords = map[string][]string{
	"blue":   {"Navy", "Blue", "Cobalt"},
	"grey":   {"Charcoal", "Grey", "Slate"},
	"green":  {"Forest", "Green", "Olive"},
	"red":    {"Crimson", "Red"},
	"white":  {"Ivory", "White"},
	"black":  {"Jet", "Black"},
	"yellow": {"Mustard", "Yellow"},
	"brown":  {"Chocolate", "Brown"},
	"pink":   {"Blush", "Pink"},
	"orange": {"Tangerine", "Orange"},
}

var colors = []string{"blue", "grey", "green", "red", "white", "black", "yellow", "brown", "pink", "orange"}

var (
	countries  = []string{"US", "GB", "DE", "FR", "CA", "AU", "PL", "NL"}
	firstNames = []string{"Ada", "Ben", "Chloe", "Dev", "Elena", "Farid", "Grace", "Hugo", "Ines", "Jonas", "Kira", "Liam"}
	lastNames  = []string{"Novak", "Okafor", "Petrov", "Quinn", "Rossi", "Sato", "Turner", "Ueda", "Vargas", "Weber"}
	domains    = []string{"gmail.com", "outlook.com", "proton.me", "fastmail.com"}
	carriers   = []string{"UPS", "DHL", "FedEx", "Royal Mail"}
)

type generator struct {
	shop *Shop
	rng  *rand.Rand
}

func pick[T any](g *generator, items []T) T { return items[g.rng.IntN(len(items))] }

// Generate creates the rows for the requested scenarios and returns one
// event per scenario. The same seed produces the same scenarios, apart from
// the IDs, which come from database sequences.
func (s *Shop) Generate(ctx context.Context, seed uint64, c Counts) ([]Scenario, error) {
	g := &generator{shop: s, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))}
	var out []Scenario
	steps := []struct {
		n    int
		make func(context.Context) (Scenario, error)
	}{
		{c.Tickets, g.ticket},
		{c.Products, g.product},
		{c.Restocks, g.restock},
		{c.Orders, g.order},
	}
	for _, step := range steps {
		for range step.n {
			sc, err := step.make(ctx)
			if err != nil {
				return out, err
			}
			out = append(out, sc)
		}
	}
	return out, nil
}

func scenario(event string, payload, expected map[string]any) Scenario {
	p, _ := json.Marshal(payload)
	e, _ := json.Marshal(expected)
	return Scenario{Event: event, Payload: p, Expected: e}
}

// customerOpts bends a generated customer towards a fraud signal.
type customerOpts struct {
	newAccount  bool
	disposable  bool
	chargebacks int
}

func (g *generator) customer(ctx context.Context, opts customerOpts) (*Customer, error) {
	first, last := pick(g, firstNames), pick(g, lastNames)
	domain := pick(g, domains)
	if opts.disposable {
		domain = pick(g, disposableDomains)
	}
	age := time.Duration(30+g.rng.IntN(900)) * 24 * time.Hour
	if opts.newAccount {
		age = time.Duration(1+g.rng.IntN(20)) * time.Hour
	}
	return one[Customer](ctx, g.shop.Pool, `
		insert into shop.customers (id, email, name, country, orders_count, chargebacks, created_at)
		values ('C-' || nextval('shop.customer_seq'), $1, $2, $3, $4, $5, $6)
		returning `+customerColumns,
		fmt.Sprintf("%s.%s%d@%s", strings.ToLower(first), strings.ToLower(last), g.rng.IntN(90)+10, domain),
		first+" "+last, pick(g, countries), g.rng.IntN(12), opts.chargebacks, time.Now().Add(-age))
}

func (g *generator) title() (title, category, color, material string) {
	pt := pick(g, productTypes)
	material = pick(g, pt.materials)
	color = pick(g, colors)
	title = fmt.Sprintf("%s %s - %s", pick(g, materialWords[material]), pt.noun, pick(g, colorWords[color]))
	return title, pt.category, color, material
}

func (g *generator) insertOrder(ctx context.Context, o Order) (*Order, error) {
	return one[Order](ctx, g.shop.Pool, `
		insert into shop.orders (id, customer_id, status, items, total_cents, billing_country, shipping_country,
			ip_country, payment_attempts, carrier, tracking, eta, placed_at)
		values ('O-' || nextval('shop.order_seq'), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		returning `+orderColumns,
		o.CustomerID, o.Status, o.Items, o.TotalCents, o.BillingCountry, o.ShippingCountry,
		o.IPCountry, o.PaymentAttempts, o.Carrier, o.Tracking, o.ETA, o.PlacedAt)
}

// plainOrder is an unremarkable order for a customer, in the given status.
func (g *generator) plainOrder(ctx context.Context, c *Customer, status string) (*Order, error) {
	title, _, _, _ := g.title()
	o := Order{
		CustomerID: c.ID, Status: status,
		Items:          fmt.Sprintf("%d x %s", 1+g.rng.IntN(3), title),
		TotalCents:     2000 + g.rng.IntN(23000),
		BillingCountry: c.Country, ShippingCountry: c.Country, IPCountry: c.Country,
		PaymentAttempts: 1,
		PlacedAt:        time.Now().Add(-time.Duration(1+g.rng.IntN(9)) * 24 * time.Hour),
	}
	if status == "shipped" || status == "delivered" {
		o.Carrier = pick(g, carriers)
		o.Tracking = fmt.Sprintf("TRK%09d", g.rng.IntN(1_000_000_000))
		o.ETA = time.Now().Add(time.Duration(1+g.rng.IntN(5)) * 24 * time.Hour).Format("2006-01-02")
	}
	return g.insertOrder(ctx, o)
}

// ticketKind is a type of support request and what should happen to it.
type ticketKind struct {
	weight      int
	orderStatus string
	// noOrder leaves the ticket without an order reference.
	noOrder bool
	action  string
	subject string
	bodies  []string
}

// The support policy: answer status questions about orders still on their
// way, and escalate everything else, including the cases that look like
// status questions but are not.
var ticketKinds = []ticketKind{
	{25, "processing", false, "reply", "Order status", []string{
		"Hi, I placed order %s a few days ago. Has it shipped yet?",
		"Could you tell me what is happening with %s? I have not had an update.",
	}},
	{25, "shipped", false, "reply", "Where is my order?", []string{
		"Hello, where is my order %s? When should I expect it?",
		"Any news on %s? I would like to know when it arrives.",
	}},
	{10, "delivered", false, "escalate", "Order not received", []string{
		"Your site says %s was delivered, but nothing has arrived. Where is it?",
		"Tracking for %s shows delivered. I have checked with my neighbours and have no parcel.",
	}},
	{15, "delivered", false, "escalate", "Refund request", []string{
		"I would like a refund for %s. It is not what I expected.",
		"Please refund order %s, I no longer need it.",
	}},
	{10, "delivered", false, "escalate", "Item arrived damaged", []string{
		"My order %s arrived broken. What can you do about it?",
	}},
	{5, "processing", false, "escalate", "Cancel my order", []string{
		"Please cancel order %s before it ships.",
	}},
	{10, "shipped", true, "escalate", "Where is my order?", []string{
		"Hi, I ordered something last week and have heard nothing. Where is it?",
		"Where is my parcel? I do not have the order number to hand.",
	}},
}

func (g *generator) ticket(ctx context.Context) (Scenario, error) {
	total := 0
	for _, k := range ticketKinds {
		total += k.weight
	}
	roll := g.rng.IntN(total)
	var kind ticketKind
	for _, k := range ticketKinds {
		if roll < k.weight {
			kind = k
			break
		}
		roll -= k.weight
	}

	c, err := g.customer(ctx, customerOpts{})
	if err != nil {
		return Scenario{}, err
	}
	o, err := g.plainOrder(ctx, c, kind.orderStatus)
	if err != nil {
		return Scenario{}, err
	}
	body := pick(g, kind.bodies)
	var orderID *string
	if !kind.noOrder {
		orderID = &o.ID
		body = fmt.Sprintf(body, o.ID)
	}
	t, err := one[Ticket](ctx, g.shop.Pool, `
		insert into shop.tickets (id, customer_id, order_id, subject, body)
		values ('T-' || nextval('shop.ticket_seq'), $1, $2, $3, $4)
		returning `+ticketColumns, c.ID, orderID, kind.subject, body)
	if err != nil {
		return Scenario{}, err
	}

	expected := map[string]any{"action": kind.action}
	if kind.action == "reply" {
		expected["order_status"] = o.Status
	}
	return scenario(EventTicketCreated, map[string]any{"ticket_id": t.ID}, expected), nil
}

func (g *generator) insertProduct(ctx context.Context, title, category, color, material string) (*Product, error) {
	return one[Product](ctx, g.shop.Pool, `
		insert into shop.products (sku, title, category, color, material, price_cents)
		values ('SKU-' || nextval('shop.product_seq'), $1, $2, $3, $4, $5)
		returning `+productColumns, title, category, color, material, 900+g.rng.IntN(9000))
}

// product creates a catalogue entry with only a title; the attributes the
// title implies are the ground truth.
func (g *generator) product(ctx context.Context) (Scenario, error) {
	title, category, color, material := g.title()
	p, err := g.insertProduct(ctx, title, "", "", "")
	if err != nil {
		return Scenario{}, err
	}
	return scenario(EventProductCreated,
		map[string]any{"sku": p.SKU},
		map[string]any{"category": category, "color": color, "material": material}), nil
}

// restock creates a stock position near its reorder point. About one in
// three is a false alarm that needs no order.
func (g *generator) restock(ctx context.Context) (Scenario, error) {
	p, err := g.insertProduct(ctx, func() string { t, _, _, _ := g.title(); return t }(), "home", "blue", "cotton")
	if err != nil {
		return Scenario{}, err
	}
	inv := Inventory{
		SKU:           p.SKU,
		DailySales:    pick(g, []float64{0.5, 1.5, 2, 3.5, 4, 6, 8.5, 12}),
		LeadTimeDays:  pick(g, []int{3, 5, 7, 10, 14}),
		CoverDays:     pick(g, []int{7, 14, 21, 30}),
		PackSize:      pick(g, []int{1, 6, 10, 12, 24, 50}),
		UnitCostCents: 200 + g.rng.IntN(3000),
	}
	reorderPoint := inv.DailySales * float64(inv.LeadTimeDays)
	if g.rng.IntN(3) == 0 {
		// Enough stock: just above the reorder point, some of it on order.
		inv.OnHand = int(math.Floor(reorderPoint)) + 1 + g.rng.IntN(15)
		inv.OnOrder = pick(g, []int{0, 0, inv.PackSize})
	} else {
		available := g.rng.IntN(int(math.Floor(reorderPoint)) + 1)
		inv.OnOrder = pick(g, []int{0, 0, 0, available / 2})
		inv.OnHand = available - inv.OnOrder
	}
	if _, err := g.shop.Pool.Exec(ctx, `
		insert into shop.inventory (sku, on_hand, on_order, daily_sales, lead_time_days, cover_days, pack_size, unit_cost_cents)
		values ($1, $2, $3, $4, $5, $6, $7, $8)`,
		inv.SKU, inv.OnHand, inv.OnOrder, inv.DailySales, inv.LeadTimeDays, inv.CoverDays, inv.PackSize, inv.UnitCostCents); err != nil {
		return Scenario{}, err
	}
	quantity := inv.ReorderQuantity()
	return scenario(EventLowStock,
		map[string]any{"sku": inv.SKU},
		map[string]any{"reorder": quantity > 0, "quantity": quantity}), nil
}

// order creates a freshly placed order carrying zero to three fraud signals.
// Roughly a third are fraudulent, and a quarter of the honest ones carry a
// single signal to tempt a false positive.
func (g *generator) order(ctx context.Context) (Scenario, error) {
	all := []string{SignalCountryMismatch, SignalPaymentRetries, SignalNewHighValue, SignalDisposableEmail, SignalChargebacks}
	g.rng.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	count := 0
	switch roll := g.rng.IntN(100); {
	case roll < 30:
		count = 2 + g.rng.IntN(2)
	case roll < 55:
		count = 1
	}
	want := map[string]bool{}
	for _, s := range all[:count] {
		want[s] = true
	}

	chargebacks := 0
	if want[SignalChargebacks] {
		chargebacks = 1 + g.rng.IntN(2)
	}
	c, err := g.customer(ctx, customerOpts{
		newAccount:  want[SignalNewHighValue],
		disposable:  want[SignalDisposableEmail],
		chargebacks: chargebacks,
	})
	if err != nil {
		return Scenario{}, err
	}

	title, _, _, _ := g.title()
	o := Order{
		CustomerID: c.ID, Status: "processing",
		Items:          fmt.Sprintf("%d x %s", 1+g.rng.IntN(3), title),
		TotalCents:     2000 + g.rng.IntN(23000),
		BillingCountry: c.Country, ShippingCountry: c.Country, IPCountry: c.Country,
		PaymentAttempts: 1 + g.rng.IntN(2),
		PlacedAt:        time.Now(),
	}
	if want[SignalNewHighValue] {
		o.TotalCents = 35000 + g.rng.IntN(60000)
	}
	if want[SignalPaymentRetries] {
		o.PaymentAttempts = 3 + g.rng.IntN(4)
	}
	if want[SignalCountryMismatch] {
		other := pick(g, countries)
		for other == c.Country {
			other = pick(g, countries)
		}
		if g.rng.IntN(2) == 0 {
			o.ShippingCountry = other
		} else {
			o.IPCountry = other
		}
	}
	placed, err := g.insertOrder(ctx, o)
	if err != nil {
		return Scenario{}, err
	}
	return scenario(EventOrderPlaced,
		map[string]any{"order_id": placed.ID},
		map[string]any{"flag": count >= 2}), nil
}
