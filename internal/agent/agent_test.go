package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wojtekmanke/roster/internal/agent"
	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/llm/llmtest"
	"github.com/wojtekmanke/roster/internal/pricing"
	"github.com/wojtekmanke/roster/internal/spec"
	"github.com/wojtekmanke/roster/internal/tool"
)

const baseSpec = `
name: tester
model: {provider: scripted, name: test-model}
instructions: Do the task.
output:
  schema: {type: object, properties: {answer: {type: string}}, required: [answer]}
`

// prices makes every scripted call cost $0.0001 * 100 input + $0.0002 * 20 output.
var prices = pricing.Table{"scripted": {"*": {Input: 100, Output: 200}}}

const costPerCall = 100*100e-6 + 20*200e-6 // $0.014

type memRecorder struct {
	mu    sync.Mutex
	steps []agent.Step
}

func (m *memRecorder) RecordStep(_ context.Context, _ string, step agent.Step) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.steps = append(m.steps, step)
	return nil
}

func (m *memRecorder) kinds() string {
	var parts []string
	for _, s := range m.steps {
		part := s.Kind + ":" + s.Name
		if s.Simulated {
			part += "(simulated)"
		}
		if s.IsError {
			part += "(error)"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " ")
}

// harness wires a scripted model, a toolset and a recorder together.
type harness struct {
	t        *testing.T
	spec     *spec.Spec
	model    *llmtest.Scripted
	recorder *memRecorder
	runner   *agent.Runner
	tools    *tool.Set
	// called counts executions per tool.
	called map[string]int
}

func newHarness(t *testing.T, specYAML string, responses ...*llm.Response) *harness {
	t.Helper()
	s, err := spec.Parse([]byte(specYAML))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, spec: s, model: llmtest.NewScripted(responses...), recorder: &memRecorder{}, called: map[string]int{}}
	h.runner = &agent.Runner{Providers: llm.Registry{"scripted": h.model}, Pricing: prices, Recorder: h.recorder}

	counted := func(name, result string) tool.Handler {
		return func(context.Context, json.RawMessage) (string, error) {
			h.called[name]++
			return result, nil
		}
	}
	orderSchema := json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)
	h.tools, err = tool.NewSet(
		tool.Tool{Name: "get_order", InputSchema: orderSchema, Handler: counted("get_order", `{"status":"shipped"}`)},
		tool.Tool{Name: "send_email", SideEffect: true, Handler: counted("send_email", "sent")},
		tool.Tool{Name: "refund_order", InputSchema: orderSchema, SideEffect: true, RequiresApproval: true, Handler: counted("refund_order", "refunded")},
		tool.Tool{Name: "broken", Handler: func(context.Context, json.RawMessage) (string, error) {
			h.called["broken"]++
			return "", errors.New("upstream is down")
		}},
		tool.Tool{Name: "slow", Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) run(mutate ...func(*agent.Run)) *agent.Outcome {
	h.t.Helper()
	run := agent.Run{ID: "run_1", Spec: h.spec, Model: h.spec.Model, Input: json.RawMessage(`{"ticket":"T-1"}`)}
	for _, m := range mutate {
		m(&run)
	}
	out, err := h.runner.Execute(context.Background(), run, h.tools)
	if err != nil {
		h.t.Fatalf("Execute: %v", err)
	}
	return out
}

// lastUserMessage returns the tool results or text most recently sent to the model.
func (h *harness) lastUserMessage() llm.Message {
	reqs := h.model.Requests()
	msgs := reqs[len(reqs)-1].Messages
	return msgs[len(msgs)-1]
}

func submit(answer string) *llm.Response {
	return llmtest.Call(spec.SubmitTool, map[string]string{"answer": answer})
}

func TestToolCallThenSubmit(t *testing.T) {
	h := newHarness(t, baseSpec,
		llmtest.Call("get_order", map[string]string{"id": "O-1"}),
		submit("shipped"),
	)
	out := h.run()

	if out.Status != agent.StatusSucceeded || string(out.Output) != `{"answer":"shipped"}` {
		t.Fatalf("outcome = %s %s (%s)", out.Status, out.Output, out.Error)
	}
	if got := h.recorder.kinds(); got != "llm:test-model tool:get_order llm:test-model" {
		t.Errorf("steps = %q", got)
	}
	for i, s := range h.recorder.steps {
		if s.Seq != i {
			t.Errorf("step %d has seq %d", i, s.Seq)
		}
	}
	totals := out.State.Totals
	if totals.LLMCalls != 2 || totals.ToolCalls != 1 {
		t.Errorf("totals = %+v", totals)
	}
	if totals.Usage.InputTokens != 200 || totals.Usage.OutputTokens != 40 {
		t.Errorf("usage = %+v", totals.Usage)
	}
	if diff := totals.CostUSD - 2*costPerCall; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("cost = %v, want %v", totals.CostUSD, 2*costPerCall)
	}

	first := h.model.Requests()[0]
	if first.System != "Do the task." {
		t.Errorf("system prompt = %q", first.System)
	}
	if !strings.Contains(first.Messages[0].Text(), `"ticket": "T-1"`) {
		t.Errorf("first message = %q, want the run input", first.Messages[0].Text())
	}
	var names []string
	for _, def := range first.Tools {
		names = append(names, def.Name)
	}
	if got := strings.Join(names, ","); !strings.HasSuffix(got, ","+spec.SubmitTool) {
		t.Errorf("tools offered = %s, want %s last", got, spec.SubmitTool)
	}
	if res := h.lastUserMessage().Content[0]; res.Type != llm.BlockToolResult || res.Text != `{"status":"shipped"}` || res.IsError {
		t.Errorf("tool result = %+v", res)
	}

	// The second model step traces only the new tool result, not the whole history.
	var traced struct{ Messages []llm.Message }
	json.Unmarshal(h.recorder.steps[2].Input, &traced)
	if len(traced.Messages) != 1 || traced.Messages[0].Content[0].Type != llm.BlockToolResult {
		t.Errorf("second model step input = %s", h.recorder.steps[2].Input)
	}
}

func TestPlainTextAgentNeedsNoSubmit(t *testing.T) {
	h := newHarness(t, "name: tester\nmodel: {provider: scripted, name: test-model}\ninstructions: Summarise.\n", llmtest.Say("All quiet."))
	out := h.run()
	if out.Status != agent.StatusSucceeded || string(out.Output) != `{"text":"All quiet."}` {
		t.Fatalf("outcome = %s %s (%s)", out.Status, out.Output, out.Error)
	}
	for _, def := range h.model.Requests()[0].Tools {
		if def.Name == spec.SubmitTool {
			t.Error("submit tool offered to an agent with no output schema")
		}
	}
}

func TestBadCallsAreReportedToTheModel(t *testing.T) {
	tests := []struct {
		name     string
		call     *llm.Response
		wantText string
		wantRuns map[string]int
	}{
		{"unknown tool", llmtest.Call("get_weather", map[string]string{}), `no tool named "get_weather"`, nil},
		{"invalid input", llmtest.Call("get_order", map[string]int{"id": 7}), "invalid input", map[string]int{"get_order": 0}},
		{"tool failure", llmtest.Call("broken", map[string]string{}), "upstream is down", map[string]int{"broken": 1}},
		{"bad submission", llmtest.Call(spec.SubmitTool, map[string]int{"answer": 1}), "does not match the required schema", nil},
		{"submission with other calls", llmtest.Calls(
			llmtest.Use("a", "get_order", map[string]string{"id": "O-1"}),
			llmtest.Use("b", spec.SubmitTool, map[string]string{"answer": "x"}),
		), "must be called on its own", map[string]int{"get_order": 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, baseSpec, tt.call, submit("recovered"))
			out := h.run()
			if out.Status != agent.StatusSucceeded {
				t.Fatalf("status = %s (%s), want the run to recover", out.Status, out.Error)
			}
			var found bool
			for _, b := range h.lastUserMessage().Content {
				if b.IsError && strings.Contains(b.Text, tt.wantText) {
					found = true
				}
			}
			if !found {
				t.Errorf("model was not told %q; got %+v", tt.wantText, h.lastUserMessage().Content)
			}
			for name, want := range tt.wantRuns {
				if h.called[name] != want {
					t.Errorf("%s ran %d times, want %d", name, h.called[name], want)
				}
			}
		})
	}
}

func TestMissingSubmissionIsRemindedOnce(t *testing.T) {
	h := newHarness(t, baseSpec, llmtest.Say("It shipped."), submit("shipped"))
	out := h.run()
	if out.Status != agent.StatusSucceeded {
		t.Fatalf("status = %s (%s)", out.Status, out.Error)
	}
	if got := h.lastUserMessage().Text(); !strings.Contains(got, "Call submit_result") {
		t.Errorf("reminder = %q", got)
	}

	h = newHarness(t, baseSpec, llmtest.Say("It shipped."), llmtest.Say("As I said, it shipped."))
	out = h.run()
	if out.Status != agent.StatusFailed || !strings.Contains(out.Error, "without calling submit_result") {
		t.Fatalf("outcome = %s (%s), want a failure after one reminder", out.Status, out.Error)
	}
}

func TestLimits(t *testing.T) {
	loop := func(n int) []*llm.Response {
		var rs []*llm.Response
		for range n {
			rs = append(rs, llmtest.Call("get_order", map[string]string{"id": "O-1"}))
		}
		return rs
	}

	t.Run("steps", func(t *testing.T) {
		h := newHarness(t, baseSpec+"limits: {max_steps: 3}\n", loop(5)...)
		out := h.run()
		if out.Status != agent.StatusFailed || !strings.Contains(out.Error, "step limit reached: 3") {
			t.Fatalf("outcome = %s (%s)", out.Status, out.Error)
		}
		if got := len(h.model.Requests()); got != 3 {
			t.Errorf("made %d model calls, want 3", got)
		}
	})

	t.Run("cost", func(t *testing.T) {
		// Each call costs $0.014, so the third call would start above $0.02.
		h := newHarness(t, baseSpec+"limits: {max_cost_usd: 0.02}\n", loop(5)...)
		out := h.run()
		if out.Status != agent.StatusFailed || !strings.Contains(out.Error, "cost limit reached") {
			t.Fatalf("outcome = %s (%s)", out.Status, out.Error)
		}
		if got := len(h.model.Requests()); got != 2 {
			t.Errorf("made %d model calls, want 2", got)
		}
	})

	t.Run("tool timeout", func(t *testing.T) {
		h := newHarness(t, baseSpec+"limits: {tool_timeout: 20ms}\n", llmtest.Call("slow", map[string]string{}), submit("gave up"))
		out := h.run()
		if out.Status != agent.StatusSucceeded {
			t.Fatalf("status = %s (%s)", out.Status, out.Error)
		}
		if res := h.lastUserMessage().Content[0]; !res.IsError || !strings.Contains(res.Text, "did not answer within 20ms") {
			t.Errorf("tool result = %+v", res)
		}
	})

	t.Run("run timeout", func(t *testing.T) {
		h := newHarness(t, baseSpec+"limits: {timeout: 30ms, tool_timeout: 10s}\n", llmtest.Call("slow", map[string]string{}))
		h.model.Func = func(req *llm.Request) (*llm.Response, error) {
			if len(req.Messages) == 1 {
				return llmtest.Call("slow", map[string]string{}), nil
			}
			return nil, context.DeadlineExceeded
		}
		out := h.run()
		if out.Status != agent.StatusFailed || !strings.Contains(out.Error, "run timed out after 30ms") {
			t.Fatalf("outcome = %s (%s)", out.Status, out.Error)
		}
	})
}

func TestModelFailures(t *testing.T) {
	t.Run("provider error", func(t *testing.T) {
		h := newHarness(t, baseSpec)
		h.model.ThenError(&llm.Error{Provider: "scripted", Status: 529, Retryable: true, Err: errors.New("overloaded")})
		out := h.run()
		if out.Status != agent.StatusFailed || !strings.Contains(out.Error, "overloaded") {
			t.Fatalf("outcome = %s (%s)", out.Status, out.Error)
		}
		if got := h.recorder.kinds(); got != "llm:test-model(error)" {
			t.Errorf("steps = %q, want the failed call in the trace", got)
		}
	})

	t.Run("refusal", func(t *testing.T) {
		h := newHarness(t, baseSpec, &llm.Response{
			Message:         llm.Message{Role: llm.RoleAssistant},
			StopReason:      llm.StopRefusal,
			RefusalCategory: "cyber",
		})
		out := h.run()
		if out.Status != agent.StatusFailed || !strings.Contains(out.Error, "declined the request (cyber)") {
			t.Fatalf("outcome = %s (%s)", out.Status, out.Error)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		h := newHarness(t, baseSpec, &llm.Response{
			Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{llm.Text("partial")}},
			StopReason: llm.StopMaxTokens,
		})
		out := h.run()
		if out.Status != agent.StatusFailed || !strings.Contains(out.Error, "max_tokens") {
			t.Fatalf("outcome = %s (%s)", out.Status, out.Error)
		}
	})

	t.Run("unknown provider", func(t *testing.T) {
		h := newHarness(t, baseSpec)
		out := h.run(func(r *agent.Run) { r.Model.Provider = "nope" })
		if out.Status != agent.StatusFailed || !strings.Contains(out.Error, `provider "nope" is not configured`) {
			t.Fatalf("outcome = %s (%s)", out.Status, out.Error)
		}
	})

	t.Run("unpriced model is flagged in the trace", func(t *testing.T) {
		h := newHarness(t, baseSpec, submit("x"))
		h.runner.Pricing = pricing.Table{}
		out := h.run()
		if out.Status != agent.StatusSucceeded || out.State.Totals.CostUSD != 0 {
			t.Fatalf("outcome = %s cost %v", out.Status, out.State.Totals.CostUSD)
		}
		if !strings.Contains(string(h.recorder.steps[0].Output), `"unpriced":true`) {
			t.Errorf("step output = %s", h.recorder.steps[0].Output)
		}
	})
}

func TestApprovalSuspendsAndResumes(t *testing.T) {
	// The model asks for a read and a refund in one turn.
	turn := llmtest.Calls(
		llmtest.Use("c1", "get_order", map[string]string{"id": "O-1"}),
		llmtest.Use("c2", "refund_order", map[string]string{"id": "O-1"}),
	)

	suspend := func(t *testing.T, h *harness) agent.State {
		t.Helper()
		out := h.run()
		if out.Status != agent.StatusAwaitingApproval {
			t.Fatalf("status = %s (%s), want awaiting_approval", out.Status, out.Error)
		}
		if len(out.Approvals) != 1 || out.Approvals[0].ToolUseID != "c2" || out.Approvals[0].Tool != "refund_order" {
			t.Fatalf("approvals = %+v", out.Approvals)
		}
		if h.called["get_order"] != 1 || h.called["refund_order"] != 0 {
			t.Fatalf("before approval: called = %v", h.called)
		}
		// The state must survive being stored as JSON.
		raw, err := json.Marshal(out.State)
		if err != nil {
			t.Fatal(err)
		}
		var restored agent.State
		if err := json.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		return restored
	}

	t.Run("approved", func(t *testing.T) {
		h := newHarness(t, baseSpec, turn, submit("refunded"))
		state := suspend(t, h)

		out := h.run(func(r *agent.Run) {
			r.State = &state
			r.Decisions = map[string]agent.Decision{"c2": {Approved: true}}
		})
		if out.Status != agent.StatusSucceeded || string(out.Output) != `{"answer":"refunded"}` {
			t.Fatalf("outcome = %s %s (%s)", out.Status, out.Output, out.Error)
		}
		if h.called["get_order"] != 1 || h.called["refund_order"] != 1 {
			t.Errorf("called = %v, want each tool exactly once", h.called)
		}
		// Both results go back in one message, in the order the model asked.
		results := h.lastUserMessage().Content
		if len(results) != 2 || results[0].ToolUseID != "c1" || results[1].ToolUseID != "c2" || results[1].Text != "refunded" {
			t.Errorf("results = %+v", results)
		}
		if out.State.Pending != nil {
			t.Error("pending turn was not cleared")
		}
		if out.State.Totals.LLMCalls != 2 || out.State.Totals.ToolCalls != 2 {
			t.Errorf("totals did not accumulate across the suspension: %+v", out.State.Totals)
		}
		if got := h.recorder.kinds(); got != "llm:test-model tool:get_order tool:refund_order llm:test-model" {
			t.Errorf("steps = %q", got)
		}
		for i, s := range h.recorder.steps {
			if s.Seq != i {
				t.Errorf("step %d has seq %d after resume", i, s.Seq)
			}
		}
	})

	t.Run("rejected", func(t *testing.T) {
		h := newHarness(t, baseSpec, turn, submit("escalated"))
		state := suspend(t, h)

		out := h.run(func(r *agent.Run) {
			r.State = &state
			r.Decisions = map[string]agent.Decision{"c2": {Approved: false, Reason: "outside the refund window"}}
		})
		if out.Status != agent.StatusSucceeded {
			t.Fatalf("status = %s (%s)", out.Status, out.Error)
		}
		if h.called["refund_order"] != 0 {
			t.Error("a rejected call was executed")
		}
		rejected := h.lastUserMessage().Content[1]
		if !rejected.IsError || !strings.Contains(rejected.Text, "outside the refund window") {
			t.Errorf("rejection = %+v", rejected)
		}
	})

	t.Run("still undecided", func(t *testing.T) {
		h := newHarness(t, baseSpec, turn)
		state := suspend(t, h)

		out := h.run(func(r *agent.Run) { r.State = &state })
		if out.Status != agent.StatusAwaitingApproval || len(out.Approvals) != 1 {
			t.Fatalf("outcome = %s %+v", out.Status, out.Approvals)
		}
		if got := len(h.model.Requests()); got != 1 {
			t.Errorf("model was called %d times while waiting, want 1", got)
		}
	})
}

func TestShadowRunSimulatesSideEffects(t *testing.T) {
	h := newHarness(t, baseSpec,
		llmtest.Calls(
			llmtest.Use("c1", "get_order", map[string]string{"id": "O-1"}),
			llmtest.Use("c2", "send_email", map[string]string{}),
			llmtest.Use("c3", "refund_order", map[string]string{"id": "O-1"}),
		),
		submit("done"),
	)
	out := h.run(func(r *agent.Run) { r.Shadow = true })

	if out.Status != agent.StatusSucceeded {
		t.Fatalf("status = %s (%s), want no approval wait in a shadow run", out.Status, out.Error)
	}
	if h.called["get_order"] != 1 {
		t.Error("read-only tool should still run in a shadow run")
	}
	if h.called["send_email"] != 0 || h.called["refund_order"] != 0 {
		t.Errorf("side effects ran in a shadow run: %v", h.called)
	}
	if got := h.recorder.kinds(); got != "llm:test-model tool:get_order tool:send_email(simulated) tool:refund_order(simulated) llm:test-model" {
		t.Errorf("steps = %q", got)
	}
	if res := h.lastUserMessage().Content[1]; res.IsError || !strings.Contains(res.Text, `"simulated":true`) {
		t.Errorf("simulated result = %+v", res)
	}
}

func TestActiveTimeUsesTheClock(t *testing.T) {
	h := newHarness(t, baseSpec, submit("x"))
	clock := time.Unix(1_700_000_000, 0)
	h.runner.Now = func() time.Time {
		clock = clock.Add(250 * time.Millisecond)
		return clock
	}
	out := h.run()
	if out.State.Totals.ActiveMS <= 0 {
		t.Fatalf("ActiveMS = %d", out.State.Totals.ActiveMS)
	}
	if h.recorder.steps[0].Latency != 250*time.Millisecond {
		t.Errorf("latency = %s", h.recorder.steps[0].Latency)
	}
}
