package shop_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wojtekmanke/roster/internal/shop"
	shopgraph "github.com/wojtekmanke/roster/internal/shop/graph"
	"github.com/wojtekmanke/roster/internal/store/storetest"
)

func newShop(t *testing.T) *shop.Shop {
	t.Helper()
	return &shop.Shop{Pool: storetest.New(t).Pool}
}

func decode(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestReorderQuantity(t *testing.T) {
	tests := []struct {
		name string
		inv  shop.Inventory
		want int
	}{
		// Sells 4 a day, 7 days to restock: reorder point 28. 20 in hand is short.
		// Need 4*(7+14) - 20 = 64, in packs of 10 -> 70.
		{"below reorder point", shop.Inventory{OnHand: 20, DailySales: 4, LeadTimeDays: 7, CoverDays: 14, PackSize: 10}, 70},
		{"stock on order counts", shop.Inventory{OnHand: 10, OnOrder: 10, DailySales: 4, LeadTimeDays: 7, CoverDays: 14, PackSize: 10}, 70},
		{"exactly at the reorder point reorders", shop.Inventory{OnHand: 28, DailySales: 4, LeadTimeDays: 7, CoverDays: 14, PackSize: 1}, 56},
		{"above the reorder point", shop.Inventory{OnHand: 29, DailySales: 4, LeadTimeDays: 7, CoverDays: 14, PackSize: 10}, 0},
		{"on order lifts it above", shop.Inventory{OnHand: 5, OnOrder: 24, DailySales: 4, LeadTimeDays: 7, CoverDays: 14, PackSize: 10}, 0},
		// 3.5*(5+7) - 17 = 25, in packs of 12 -> 36.
		{"fractional sales", shop.Inventory{OnHand: 17, DailySales: 3.5, LeadTimeDays: 5, CoverDays: 7, PackSize: 12}, 36},
		{"exact multiple of the pack", shop.Inventory{OnHand: 0, DailySales: 2, LeadTimeDays: 5, CoverDays: 7, PackSize: 24}, 24},
	}
	for _, tt := range tests {
		if got := tt.inv.ReorderQuantity(); got != tt.want {
			t.Errorf("%s: ReorderQuantity() = %d, want %d", tt.name, got, tt.want)
		}
	}
}

// TestGroundTruthMatchesTheData checks every generated answer against the
// rows it was generated with, using the policies as the agents are told them.
func TestGroundTruthMatchesTheData(t *testing.T) {
	s := newShop(t)
	ctx := context.Background()
	scenarios, err := s.Generate(ctx, 7, shop.Counts{Tickets: 80, Products: 60, Restocks: 80, Orders: 120})
	if err != nil {
		t.Fatal(err)
	}
	if len(scenarios) != 340 {
		t.Fatalf("generated %d scenarios, want 340", len(scenarios))
	}

	seen := map[string]int{}
	for _, sc := range scenarios {
		payload, expected := decode(t, sc.Payload), decode(t, sc.Expected)
		switch sc.Event {
		case shop.EventTicketCreated:
			ticket, err := s.Ticket(ctx, payload["ticket_id"].(string))
			if err != nil {
				t.Fatal(err)
			}
			action := expected["action"].(string)
			seen["ticket:"+action]++
			if action == "reply" {
				if ticket.OrderID == nil {
					t.Fatalf("ticket %s expects a reply but names no order", ticket.ID)
				}
				order, _ := s.Order(ctx, *ticket.OrderID)
				if order.Status != "processing" && order.Status != "shipped" {
					t.Errorf("ticket %s expects a reply but its order is %s", ticket.ID, order.Status)
				}
				if expected["order_status"] != order.Status {
					t.Errorf("ticket %s: expected status %v, order is %s", ticket.ID, expected["order_status"], order.Status)
				}
				if !strings.Contains(ticket.Body, order.ID) {
					t.Errorf("ticket %s does not mention its order: %q", ticket.ID, ticket.Body)
				}
			} else if ticket.OrderID == nil {
				seen["ticket:no_order"]++
			}

		case shop.EventProductCreated:
			p, err := s.Product(ctx, payload["sku"].(string))
			if err != nil {
				t.Fatal(err)
			}
			if p.Category != "" || p.Color != "" || p.Material != "" {
				t.Errorf("product %s was created with attributes already filled in", p.SKU)
			}
			for _, field := range []string{"category", "color", "material"} {
				if v, _ := expected[field].(string); v == "" {
					t.Errorf("product %s has no expected %s", p.SKU, field)
				}
			}
			seen["product:"+expected["category"].(string)]++

		case shop.EventLowStock:
			inv, err := s.Inventory(ctx, payload["sku"].(string))
			if err != nil {
				t.Fatal(err)
			}
			if inv.OnHand < 0 || inv.OnOrder < 0 {
				t.Errorf("inventory %s has negative stock: %+v", inv.SKU, inv)
			}
			want := inv.ReorderQuantity()
			if expected["quantity"] != float64(want) || expected["reorder"] != (want > 0) {
				t.Errorf("inventory %s: expected %v, policy gives %d", inv.SKU, expected, want)
			}
			if want%inv.PackSize != 0 {
				t.Errorf("inventory %s: quantity %d is not a multiple of pack size %d", inv.SKU, want, inv.PackSize)
			}
			if want > 0 {
				seen["restock:yes"]++
			} else {
				seen["restock:no"]++
			}

		case shop.EventOrderPlaced:
			order, err := s.Order(ctx, payload["order_id"].(string))
			if err != nil {
				t.Fatal(err)
			}
			customer, _ := s.Customer(ctx, order.CustomerID)
			signals := shop.RiskSignals(order, customer)
			if expected["flag"] != (len(signals) >= 2) {
				t.Errorf("order %s: expected flag=%v but it carries signals %v", order.ID, expected["flag"], signals)
			}
			if order.Status != "processing" {
				t.Errorf("order %s is %s, want processing", order.ID, order.Status)
			}
			switch {
			case len(signals) >= 2:
				seen["order:fraud"]++
			case len(signals) == 1:
				seen["order:one_signal"]++
			default:
				seen["order:clean"]++
			}
		}
	}

	// Every interesting case must actually occur, or the scorecards measure nothing.
	for _, key := range []string{
		"ticket:reply", "ticket:escalate", "ticket:no_order",
		"restock:yes", "restock:no",
		"order:fraud", "order:one_signal", "order:clean",
		"product:apparel", "product:drinkware", "product:bags", "product:stationery", "product:home",
	} {
		if seen[key] == 0 {
			t.Errorf("no scenario of kind %q was generated", key)
		}
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	ctx := context.Background()
	answers := func() string {
		scenarios, err := newShop(t).Generate(ctx, 42, shop.Counts{Tickets: 10, Products: 10, Restocks: 10, Orders: 10})
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, sc := range scenarios {
			b.WriteString(sc.Event + string(sc.Expected) + "\n")
		}
		return b.String()
	}
	if first, second := answers(), answers(); first != second {
		t.Fatalf("the same seed produced different answers:\n%s\n---\n%s", first, second)
	}
}

type shopAPI struct {
	t     *testing.T
	url   string
	token string
}

func (a *shopAPI) query(q string, vars map[string]any) (map[string]any, []string) {
	a.t.Helper()
	body, _ := json.Marshal(map[string]any{"query": q, "variables": vars})
	req, _ := http.NewRequest("POST", a.url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, []string{"unauthorized"}
	}
	var out struct {
		Data   map[string]any
		Errors []struct{ Message string }
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		a.t.Fatalf("unreadable response %q", raw)
	}
	var errs []string
	for _, e := range out.Errors {
		errs = append(errs, e.Message)
	}
	return out.Data, errs
}

func (a *shopAPI) must(q string, vars map[string]any) map[string]any {
	a.t.Helper()
	data, errs := a.query(q, vars)
	if len(errs) > 0 {
		a.t.Fatalf("errors %v for %s", errs, q)
	}
	return data
}

func TestGraphQLAPI(t *testing.T) {
	s := newShop(t)
	ctx := context.Background()
	scenarios, err := s.Generate(ctx, 3, shop.Counts{Tickets: 30, Products: 1, Restocks: 1, Orders: 1})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(shopgraph.NewHandler(s, "shop-token"))
	defer srv.Close()
	api := &shopAPI{t: t, url: srv.URL, token: "shop-token"}

	if _, errs := (&shopAPI{t: t, url: srv.URL}).query(`{ dailySummary { openTickets } }`, nil); len(errs) != 1 || errs[0] != "unauthorized" {
		t.Fatalf("request without a token: %v", errs)
	}

	// Find a ticket that names an order, and fetch it with everything an
	// agent needs in one request.
	var ticketID string
	for _, sc := range scenarios[:30] {
		if decode(t, sc.Expected)["action"] == "reply" {
			ticketID = decode(t, sc.Payload)["ticket_id"].(string)
			break
		}
	}
	data := api.must(`query($id: ID!) { ticket(id: $id) { id subject body status
		customer { name email accountAgeDays }
		order { id status carrier tracking eta totalCents customer { country } } } }`, map[string]any{"id": ticketID})
	ticket := data["ticket"].(map[string]any)
	order := ticket["order"].(map[string]any)
	if ticket["status"] != "open" || order["id"] == "" || ticket["customer"].(map[string]any)["email"] == "" {
		t.Fatalf("ticket = %v", ticket)
	}
	if order["customer"].(map[string]any)["country"] == "" {
		t.Errorf("nested order.customer missing: %v", order)
	}

	replied := api.must(`mutation($id: ID!) { replyToTicket(id: $id, body: "It has shipped.") { status reply } }`, map[string]any{"id": ticketID})
	if r := replied["replyToTicket"].(map[string]any); r["status"] != "answered" || r["reply"] != "It has shipped." {
		t.Errorf("reply = %v", r)
	}
	if _, errs := api.query(`mutation($id: ID!) { escalateTicket(id: $id, reason: "x") { id } }`, map[string]any{"id": ticketID}); len(errs) != 1 || !strings.Contains(errs[0], "already answered") {
		t.Errorf("escalating an answered ticket: %v", errs)
	}

	sku := decode(t, scenarios[30].Payload)["sku"].(string)
	updated := api.must(`mutation($sku: ID!) { updateProduct(sku: $sku, color: "blue", description: "A mug.") { title color category description } }`, map[string]any{"sku": sku})
	if p := updated["updateProduct"].(map[string]any); p["color"] != "blue" || p["category"] != "" || p["description"] != "A mug." {
		t.Errorf("updateProduct = %v, want only the given fields changed", p)
	}

	stockSKU := decode(t, scenarios[31].Payload)["sku"].(string)
	before := api.must(`query($sku: ID!) { inventory(sku: $sku) { onHand onOrder dailySales leadTimeDays coverDays packSize } }`, map[string]any{"sku": stockSKU})
	api.must(`mutation($sku: ID!) { createPurchaseOrder(sku: $sku, quantity: 24, note: "restock") { id quantity } }`, map[string]any{"sku": stockSKU})
	after := api.must(`query($sku: ID!) { inventory(sku: $sku) { onOrder } }`, map[string]any{"sku": stockSKU})
	if got, want := after["inventory"].(map[string]any)["onOrder"].(float64), before["inventory"].(map[string]any)["onOrder"].(float64)+24; got != want {
		t.Errorf("onOrder = %v after a purchase order, want %v", got, want)
	}
	if _, errs := api.query(`mutation($sku: ID!) { createPurchaseOrder(sku: $sku, quantity: 0) { id } }`, map[string]any{"sku": stockSKU}); len(errs) != 1 {
		t.Errorf("zero-quantity purchase order: %v", errs)
	}

	orderID := decode(t, scenarios[32].Payload)["order_id"].(string)
	held := api.must(`mutation($id: ID!) { holdOrder(id: $id, reason: "two risk signals") { status holdReason } }`, map[string]any{"id": orderID})
	if h := held["holdOrder"].(map[string]any); h["status"] != "on_hold" || h["holdReason"] != "two risk signals" {
		t.Errorf("holdOrder = %v", h)
	}
	if _, errs := api.query(`mutation($id: ID!) { holdOrder(id: $id, reason: "again") { id } }`, map[string]any{"id": orderID}); len(errs) != 1 || !strings.Contains(errs[0], "cannot be put on hold") {
		t.Errorf("holding an order twice: %v", errs)
	}

	summary := api.must(`{ dailySummary { ordersPlaced revenueCents openTickets escalatedTickets ordersOnHold skusToReorder } }`, nil)["dailySummary"].(map[string]any)
	if summary["openTickets"] != float64(29) || summary["ordersOnHold"] != float64(1) || summary["ordersPlaced"].(float64) < 1 {
		t.Errorf("summary = %v", summary)
	}

	missing := api.must(`{ ticket(id: "T-0") { id } order(id: "O-0") { id } product(sku: "X") { sku } inventory(sku: "X") { sku } customer(id: "C-0") { id } }`, nil)
	for name, v := range missing {
		if v != nil {
			t.Errorf("%s = %v, want null", name, v)
		}
	}
	if _, errs := api.query(`mutation { holdOrder(id: "O-0", reason: "x") { id } }`, nil); len(errs) != 1 || errs[0] != "no such record" {
		t.Errorf("mutation on a missing order: %v", errs)
	}
}
