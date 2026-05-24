package agents_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/wojtekmanke/roster/agents"
	"github.com/wojtekmanke/roster/internal/agent"
	"github.com/wojtekmanke/roster/internal/fleet"
	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/llm/llmtest"
	"github.com/wojtekmanke/roster/internal/pricing"
	"github.com/wojtekmanke/roster/internal/scoring"
	"github.com/wojtekmanke/roster/internal/shop"
	shopgraph "github.com/wojtekmanke/roster/internal/shop/graph"
	"github.com/wojtekmanke/roster/internal/spec"
	"github.com/wojtekmanke/roster/internal/store"
	"github.com/wojtekmanke/roster/internal/store/storetest"
	"github.com/wojtekmanke/roster/internal/tool"
)

func TestExamplesAreValid(t *testing.T) {
	examples, err := agents.Examples()
	if err != nil {
		t.Fatal(err)
	}
	if len(examples) != 5 {
		t.Fatalf("found %d example specs, want 5", len(examples))
	}
	graders := scoring.Builtin()
	graders["judge"] = scoring.Judge{}
	for file, data := range examples {
		s, err := spec.Parse(data)
		if err != nil {
			t.Errorf("%s: %v", file, err)
			continue
		}
		if s.Name+".yaml" != file {
			t.Errorf("%s declares the name %q; file and agent names should match", file, s.Name)
		}
		for _, kpi := range s.KPIs {
			if _, ok := graders[kpi.Grader]; !ok {
				t.Errorf("%s: kpi %q uses unknown grader %q", file, kpi.Name, kpi.Grader)
			}
		}
	}
}

// The tests below run the example agents end to end against the demo store,
// with the models replaced by two scripted stand-ins: an oracle that follows
// each agent's instructions exactly, and a careless model that answers
// without looking. Everything else is real: the specs, the GraphQL tools, the
// store's API, the queue, approvals, grading and the scorecards.

var idPattern = regexp.MustCompile(`(T|O|SKU)-\d+`)

// conversation summarises where a scripted model is in its work.
type conversation struct {
	subject string            // the ID named in the first message
	results map[string]string // tool name -> its latest result
	offered map[string]bool   // tools on offer
}

func read(req *llm.Request) conversation {
	c := conversation{results: map[string]string{}, offered: map[string]bool{}}
	c.subject = idPattern.FindString(req.Messages[0].Text())
	for _, def := range req.Tools {
		c.offered[def.Name] = true
	}
	names := map[string]string{}
	for _, m := range req.Messages {
		for _, b := range m.Content {
			switch b.Type {
			case llm.BlockToolUse:
				names[b.ToolUseID] = b.ToolName
			case llm.BlockToolResult:
				c.results[names[b.ToolUseID]] = b.Text
			}
		}
	}
	return c
}

func call(name string, input map[string]any) (*llm.Response, error) {
	return llmtest.Call(name, input), nil
}

// oracle plays every example agent correctly, and the judge generously.
func oracle(t *testing.T) func(*llm.Request) (*llm.Response, error) {
	return func(req *llm.Request) (*llm.Response, error) {
		c := read(req)
		switch {
		case c.offered["grade"]:
			return call("grade", map[string]any{"score": 5, "reasoning": "Clear and accurate."})

		case c.offered["get_ticket"]:
			raw, fetched := c.results["get_ticket"]
			if !fetched {
				return call("get_ticket", map[string]any{"id": c.subject})
			}
			var data struct {
				Ticket struct {
					Subject string
					Order   *struct{ ID, Status string }
				}
			}
			mustDecode(t, raw, &data)
			asksWhere := data.Ticket.Subject == "Order status" || data.Ticket.Subject == "Where is my order?"
			order := data.Ticket.Order
			if asksWhere && order != nil && (order.Status == "processing" || order.Status == "shipped") {
				if _, done := c.results["reply_to_ticket"]; !done {
					return call("reply_to_ticket", map[string]any{"id": c.subject, "body": "Your order is " + order.Status + "."})
				}
				return call(spec.SubmitTool, map[string]any{"action": "reply", "order_status": order.Status, "summary": "Answered a status question."})
			}
			if _, done := c.results["escalate_ticket"]; !done {
				return call("escalate_ticket", map[string]any{"id": c.subject, "reason": "Not a status question we can answer."})
			}
			return call(spec.SubmitTool, map[string]any{"action": "escalate", "summary": "Escalated."})

		case c.offered["get_product"]:
			raw, fetched := c.results["get_product"]
			if !fetched {
				return call("get_product", map[string]any{"sku": c.subject})
			}
			var data struct{ Product struct{ Title string } }
			mustDecode(t, raw, &data)
			category, color, material, ok := shop.TitleAttributes(data.Product.Title)
			if !ok {
				return nil, fmt.Errorf("oracle cannot read the title %q", data.Product.Title)
			}
			fields := map[string]any{"category": category, "color": color, "material": material,
				"description": fmt.Sprintf("A %s %s item made of %s.", color, category, material)}
			if _, done := c.results["update_product"]; !done {
				fields["sku"] = c.subject
				return call("update_product", fields)
			}
			return call(spec.SubmitTool, fields)

		case c.offered["get_inventory"]:
			raw, fetched := c.results["get_inventory"]
			if !fetched {
				return call("get_inventory", map[string]any{"sku": c.subject})
			}
			var data struct {
				Inventory struct {
					OnHand, OnOrder, LeadTimeDays, CoverDays, PackSize int
					DailySales                                         float64
				}
			}
			mustDecode(t, raw, &data)
			i := data.Inventory
			inv := shop.Inventory{OnHand: i.OnHand, OnOrder: i.OnOrder, DailySales: i.DailySales, LeadTimeDays: i.LeadTimeDays, CoverDays: i.CoverDays, PackSize: i.PackSize}
			quantity := inv.ReorderQuantity()
			if _, done := c.results["create_purchase_order"]; quantity > 0 && !done {
				return call("create_purchase_order", map[string]any{"sku": c.subject, "quantity": quantity, "note": "Per policy."})
			}
			return call(spec.SubmitTool, map[string]any{"reorder": quantity > 0, "quantity": quantity, "reasoning": "Per policy."})

		case c.offered["get_order"]:
			raw, fetched := c.results["get_order"]
			if !fetched {
				return call("get_order", map[string]any{"id": c.subject})
			}
			var data struct {
				Order struct {
					TotalCents, PaymentAttempts                int
					BillingCountry, ShippingCountry, IPCountry string
					Customer                                   struct {
						Email                       string
						AccountAgeDays, Chargebacks int
					}
				}
			}
			mustDecode(t, raw, &data)
			o := data.Order
			signals := []string{}
			if o.BillingCountry != o.ShippingCountry || o.IPCountry != o.BillingCountry {
				signals = append(signals, "country_mismatch")
			}
			if o.PaymentAttempts >= 3 {
				signals = append(signals, "payment_retries")
			}
			if o.Customer.AccountAgeDays < 2 && o.TotalCents > 30000 {
				signals = append(signals, "new_account_high_value")
			}
			for _, d := range []string{"mailinator.com", "tempmail.dev", "10minutemail.com", "guerrillamail.com"} {
				if strings.HasSuffix(o.Customer.Email, "@"+d) {
					signals = append(signals, "disposable_email")
				}
			}
			if o.Customer.Chargebacks >= 1 {
				signals = append(signals, "prior_chargebacks")
			}
			flag := len(signals) >= 2
			if _, done := c.results["hold_order"]; flag && !done {
				return call("hold_order", map[string]any{"id": c.subject, "reason": strings.Join(signals, ", ")})
			}
			return call(spec.SubmitTool, map[string]any{"flag": flag, "signals": signals})

		case c.offered["daily_summary"]:
			if raw, fetched := c.results["daily_summary"]; fetched {
				return llmtest.Say("Summary: " + raw), nil
			}
			return call("daily_summary", map[string]any{})
		}
		return nil, fmt.Errorf("oracle does not know this agent (tools: %v)", c.offered)
	}
}

// careless answers at once without looking anything up. It also tries the
// side effects, which a shadow run must not carry out.
func careless(req *llm.Request) (*llm.Response, error) {
	c := read(req)
	acted := len(req.Messages) > 1
	switch {
	case c.offered["get_ticket"]:
		if !acted {
			return call("reply_to_ticket", map[string]any{"id": c.subject, "body": "It is on its way!"})
		}
		return call(spec.SubmitTool, map[string]any{"action": "reply", "order_status": "shipped", "summary": "Replied."})
	case c.offered["get_product"]:
		fields := map[string]any{"category": "apparel", "color": "black", "material": "cotton", "description": "Nice."}
		if !acted {
			fields["sku"] = c.subject
			return call("update_product", fields)
		}
		return call(spec.SubmitTool, fields)
	case c.offered["get_inventory"]:
		if !acted {
			return call("create_purchase_order", map[string]any{"sku": c.subject, "quantity": 1000, "note": "Just in case."})
		}
		return call(spec.SubmitTool, map[string]any{"reorder": true, "quantity": 1000, "reasoning": "Just in case."})
	case c.offered["get_order"]:
		if !acted {
			return call("hold_order", map[string]any{"id": c.subject, "reason": "Looks odd."})
		}
		return call(spec.SubmitTool, map[string]any{"flag": true, "signals": []string{}})
	}
	return nil, fmt.Errorf("careless model does not know this agent")
}

func mustDecode(t *testing.T, raw string, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), into); err != nil {
		t.Errorf("tool result is not the JSON the agent expects: %v\n%s", err, raw)
	}
}

func TestExampleAgentsEndToEnd(t *testing.T) {
	ctx := context.Background()
	db := storetest.New(t)
	demo := &shop.Shop{Pool: db.Pool}

	shopAPI := httptest.NewServer(shopgraph.NewHandler(demo, "shop-token"))
	defer shopAPI.Close()
	env := map[string]string{"SHOP_GRAPHQL_URL": shopAPI.URL, "SHOP_API_TOKEN": "shop-token"}

	good := llmtest.NewScripted()
	good.Func = oracle(t)
	bad := llmtest.NewScripted()
	bad.Func = careless
	providers := llm.Registry{"anthropic": good, "xai": bad, "openai": bad, "ollama": bad}

	graders := scoring.Builtin()
	graders["judge"] = scoring.Judge{Providers: providers}
	svc := &fleet.Service{
		Store:   db,
		Runner:  &agent.Runner{Providers: providers, Pricing: pricing.Default(), Recorder: db},
		Tools:   &tool.Builder{Getenv: func(k string) string { return env[k] }},
		Graders: graders,
		Rand:    func() float64 { return 0 }, // shadow every run
	}

	examples, err := agents.Examples()
	if err != nil {
		t.Fatal(err)
	}
	for file, data := range examples {
		if _, _, err := svc.Apply(ctx, data, "test"); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}

	const each = 12
	scenarios, err := demo.Generate(ctx, 2026, shop.Counts{Tickets: each, Products: each, Restocks: each, Orders: each})
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range scenarios {
		if n, err := svc.Emit(ctx, sc.Event, sc.Payload, sc.Expected); err != nil || n != 1 {
			t.Fatalf("emit %s: queued %d, %v", sc.Event, n, err)
		}
	}
	if err := svc.Trigger(ctx, "daily-brief", fleet.TriggerWebhook, nil, nil); err != nil {
		t.Fatal(err)
	}

	// Work the queue, approving purchase orders as they come up, until idle.
	approved := 0
	for {
		worked, err := svc.Drain(ctx, "test", 0)
		if err != nil {
			t.Fatal(err)
		}
		pending, err := db.Approvals(ctx, store.ApprovalFilter{Status: store.ApprovalPending, Limit: 500})
		if err != nil {
			t.Fatal(err)
		}
		for _, ap := range pending {
			if ap.Tool != "create_purchase_order" {
				t.Errorf("unexpected approval request for %s", ap.Tool)
			}
			if _, err := svc.Decide(ctx, ap.ID, true, "test", ""); err != nil {
				t.Fatal(err)
			}
			approved++
		}
		if worked == 0 && len(pending) == 0 {
			break
		}
	}

	// Every primary run finished well.
	runs, err := db.Runs(ctx, store.RunFilter{PrimaryOnly: true, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 4*each+1 {
		t.Fatalf("%d primary runs, want %d", len(runs), 4*each+1)
	}
	for _, r := range runs {
		if r.Status != store.RunSucceeded {
			t.Errorf("run %s (%s) is %s: %s", r.ID, r.Trigger, r.Status, r.Error)
		}
		if r.CostUSD <= 0 {
			t.Errorf("run %s has no cost: is %s/%s missing from the price table?", r.ID, r.Provider, r.Model)
		}
	}

	// kpis returns an agent's scorecard values on one model.
	kpis := func(name, model string) map[string]float64 {
		t.Helper()
		a, err := db.Agent(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if a.Status != store.AgentActive {
			t.Errorf("%s is %s (%s), want active", name, a.Status, a.StatusReason)
		}
		sp, _ := a.Spec()
		cmp, err := svc.Comparison(ctx, a, sp)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range cmp {
			if m.Model != model {
				continue
			}
			out := map[string]float64{"runs": float64(m.Runs)}
			for _, k := range m.Scorecard.KPIs {
				if k.Samples > 0 {
					out[k.Name] = k.Value
				}
			}
			return out
		}
		t.Fatalf("%s has no runs on %s (have %+v)", name, model, cmp)
		return nil
	}
	perfect := func(agentName string, got map[string]float64, names ...string) {
		t.Helper()
		for _, n := range names {
			if v, ok := got[n]; !ok || v != 1 {
				t.Errorf("%s: %s = %v (scored %v), want 1", agentName, n, v, ok)
			}
		}
	}

	triage := kpis("support-triage", "claude-opus-5-5")
	perfect("support-triage", triage, "right_call", "status_correct", "escalations_caught")
	catalog := kpis("catalog-enrichment", "claude-opus-5-5")
	perfect("catalog-enrichment", catalog, "all_attributes_right", "attribute_accuracy")
	if catalog["description_quality"] != 5 {
		t.Errorf("catalog-enrichment: description_quality = %v, want the judge's 5", catalog["description_quality"])
	}
	reorder := kpis("reorder-watcher", "claude-opus-5-5")
	perfect("reorder-watcher", reorder, "right_decision", "right_quantity", "approval_rate")
	fraud := kpis("fraud-screen", "claude-opus-5-5")
	perfect("fraud-screen", fraud, "precision", "recall")
	if brief := kpis("daily-brief", "claude-opus-5-5"); brief["delivered"] != 1 || brief["runs"] != 1 {
		t.Errorf("daily-brief = %v", brief)
	}

	// The careless shadows were graded on the same work and did worse.
	for _, shadow := range []struct{ agent, model, kpi string }{
		{"support-triage", "grok-4.3", "right_call"},
		{"support-triage", "llama3.2", "right_call"},
		{"catalog-enrichment", "gpt-6-luna", "all_attributes_right"},
		{"reorder-watcher", "grok-4.7", "right_quantity"},
		{"fraud-screen", "gpt-6.1-sol", "precision"},
	} {
		got := kpis(shadow.agent, shadow.model)
		if got["runs"] != each {
			t.Errorf("%s on %s: %v shadow runs, want %d", shadow.agent, shadow.model, got["runs"], each)
		}
		if v, ok := got[shadow.kpi]; !ok || v >= 1 {
			t.Errorf("%s on %s: %s = %v (scored %v), want below 1", shadow.agent, shadow.model, shadow.kpi, v, ok)
		}
	}

	// The store shows the oracle's work and none of the shadows'.
	count := func(sql string) int {
		t.Helper()
		var n int
		if err := db.Pool.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`select count(*) from shop.tickets where status = 'open'`); n != 0 {
		t.Errorf("%d tickets were left open", n)
	}
	if n := count(`select count(*) from shop.tickets where reply = 'It is on its way!'`); n != 0 {
		t.Errorf("%d tickets carry a shadow run's reply", n)
	}
	// The restock scenarios create finished products of their own, so only
	// products that started blank are checked.
	if n := count(`select count(*) from shop.products where description = ''`); n != each {
		t.Errorf("%d products have no description, want only the %d restock products", n, each)
	}
	if n := count(`select count(*) from shop.products where description = 'Nice.'`); n != 0 {
		t.Errorf("%d products carry a shadow run's description", n)
	}
	wantPOs := 0
	wantHolds := 0
	for _, sc := range scenarios {
		var expected struct {
			Reorder, Flag bool
		}
		json.Unmarshal(sc.Expected, &expected)
		if expected.Reorder {
			wantPOs++
		}
		if expected.Flag {
			wantHolds++
		}
	}
	if n := count(`select count(*) from shop.purchase_orders`); n != wantPOs || approved != wantPOs {
		t.Errorf("%d purchase orders after %d approvals, want %d of each", n, approved, wantPOs)
	}
	if n := count(`select count(*) from shop.purchase_orders where quantity = 1000`); n != 0 {
		t.Errorf("%d purchase orders came from a shadow run", n)
	}
	if n := count(`select count(*) from shop.orders where status = 'on_hold'`); n != wantHolds {
		t.Errorf("%d orders on hold, want %d", n, wantHolds)
	}
	if wantPOs == 0 || wantHolds == 0 {
		t.Fatalf("seed produced %d reorders and %d fraudulent orders; pick one that exercises both", wantPOs, wantHolds)
	}

	// The brief was written from the store's real numbers.
	briefRuns, _ := db.Runs(ctx, store.RunFilter{Status: store.RunSucceeded, Limit: 500})
	var brief string
	for _, r := range briefRuns {
		if r.Trigger == fleet.TriggerWebhook {
			brief = string(r.Output)
		}
	}
	if !strings.Contains(brief, "ordersPlaced") {
		t.Errorf("brief = %s", brief)
	}
}
