// Package demo provides scripted stand-ins for language models, so the
// bundled examples can be explored without any API key.
//
// They are not models and their scores say nothing about any real model.
// "scripted-careful" follows each example agent's instructions exactly;
// "scripted-careless" answers without looking anything up. Between them they
// make every part of Roster visible: traces, approvals, scorecards, a model
// comparison with a clear loser, and an agent getting paused.
package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/shop"
	"github.com/wojtekmanke/roster/internal/spec"
)

// Provider and model names used by demo specs.
const (
	Name     = "demo"
	Careful  = "scripted-careful"
	Careless = "scripted-careless"
)

// Provider implements llm.Provider with the scripted models.
type Provider struct{}

// Complete implements llm.Provider.
func (Provider) Complete(_ context.Context, req *llm.Request) (*llm.Response, error) {
	var resp *llm.Response
	var err error
	switch req.Model.Name {
	case Careful:
		resp, err = careful(req)
	case Careless:
		resp, err = careless(req)
	default:
		return nil, fmt.Errorf("demo: no scripted model named %q (have %s and %s)", req.Model.Name, Careful, Careless)
	}
	if err != nil {
		return nil, err
	}
	resp.Model = req.Model.Name
	// Rough token counts, so cost and usage columns have something to show.
	for _, m := range req.Messages {
		for _, b := range m.Content {
			resp.Usage.InputTokens += int64(len(b.Text)+len(b.Input)) / 4
		}
	}
	resp.Usage.InputTokens += int64(len(req.System)) / 4
	for _, b := range resp.Message.Content {
		resp.Usage.OutputTokens += int64(len(b.Text)+len(b.Input))/4 + 1
	}
	return resp, nil
}

// Rewrite returns the spec with its models swapped for the scripted ones.
// The agent runs on the careful model and every shadow becomes the careless
// one; with careless set, the two trade places, which gives a demo an agent
// that misses its targets while its shadow would have met them. Judges always
// use the careful model. Nothing in a rewritten spec calls a real provider.
func Rewrite(data []byte, careless bool) ([]byte, error) {
	s, err := spec.Parse(data)
	if err != nil {
		return nil, err
	}
	primary, shadow := Careful, Careless
	if careless {
		primary, shadow = Careless, Careful
	}
	s.Model = llm.ModelConfig{Provider: Name, Name: primary}
	if len(s.Shadow) > 0 {
		s.Shadow = []spec.Shadow{{Model: llm.ModelConfig{Provider: Name, Name: shadow}, SampleRate: 1}}
	}
	for i := range s.KPIs {
		if s.KPIs[i].Grader == "judge" {
			s.KPIs[i].Params["model"] = map[string]any{"provider": Name, "name": Careful}
		}
	}
	return yaml.Marshal(s)
}

func toolCall(name string, input map[string]any) *llm.Response {
	raw, _ := json.Marshal(input)
	return &llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{llm.ToolUse("call_"+name, name, raw)}},
		StopReason: llm.StopToolUse,
	}
}

func say(text string) *llm.Response {
	return &llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{llm.Text(text)}},
		StopReason: llm.StopEndTurn,
	}
}

func decode(raw string, into any) error {
	if err := json.Unmarshal([]byte(raw), into); err != nil {
		return fmt.Errorf("demo: tool result is not the JSON the agent expects: %w", err)
	}
	return nil
}

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
	return toolCall(name, input), nil
}

// careful plays every example agent by its instructions. Asked to judge, it
// applies a crude version of the catalogue rubric.
func careful(req *llm.Request) (*llm.Response, error) {
	c := read(req)
	switch {
	case c.offered["grade"]:
		// As a judge it can tell a description from a shrug, and no more.
		if strings.Contains(req.Messages[0].Text(), "made of") {
			return call("grade", map[string]any{"score": 5, "reasoning": "Names the product, its material and its colour."})
		}
		return call("grade", map[string]any{"score": 1, "reasoning": "Does not describe the product."})

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
		if err := decode(raw, &data); err != nil {
			return nil, err
		}
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
		if err := decode(raw, &data); err != nil {
			return nil, err
		}
		category, color, material, ok := shop.TitleAttributes(data.Product.Title)
		if !ok {
			return nil, fmt.Errorf("demo: cannot read the title %q", data.Product.Title)
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
		if err := decode(raw, &data); err != nil {
			return nil, err
		}
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
		if err := decode(raw, &data); err != nil {
			return nil, err
		}
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
			return say("Summary: " + raw), nil
		}
		return call("daily_summary", map[string]any{})
	}
	return nil, fmt.Errorf("demo: the scripted models only know the bundled example agents")
}

// careless answers at once without looking anything up. It also attempts the
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
	return nil, fmt.Errorf("demo: the scripted models only know the bundled example agents")
}
