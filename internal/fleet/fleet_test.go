package fleet_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wojtekmanke/roster/internal/agent"
	"github.com/wojtekmanke/roster/internal/fleet"
	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/llm/llmtest"
	"github.com/wojtekmanke/roster/internal/pricing"
	"github.com/wojtekmanke/roster/internal/scoring"
	"github.com/wojtekmanke/roster/internal/spec"
	"github.com/wojtekmanke/roster/internal/store"
	"github.com/wojtekmanke/roster/internal/store/storetest"
	"github.com/wojtekmanke/roster/internal/tool"
)

// triageSpec is an agent that looks a ticket up and decides what to do with
// it. Its accuracy is judged against ground truth attached to each event.
const triageSpec = `
name: triage
model: {provider: scripted, name: primary-model}
instructions: Decide what to do with the ticket.
triggers:
  - event: ticket.created
tools:
  - {name: get_ticket, builtin: get_ticket}
  - {name: refund, builtin: refund}
output:
  schema: {type: object, properties: {action: {type: string}}, required: [action]}
kpis:
  - {name: accuracy, grader: expected, target: ">= 0.75"}
  - {name: cost, grader: cost, target: "<= 1"}
  - {name: approval_rate, grader: approvals}
review: {window: 10, min_runs: 3}
`

type env struct {
	t       *testing.T
	ctx     context.Context
	store   *store.Store
	svc     *fleet.Service
	primary *llmtest.Scripted
	shadow  *llmtest.Scripted

	mu     sync.Mutex
	called map[string]int
	clock  time.Time
}

// answerWith makes a model look the ticket up, then submit a fixed action.
func answerWith(action string) func(*llm.Request) (*llm.Response, error) {
	return func(req *llm.Request) (*llm.Response, error) {
		if len(req.Messages) == 1 {
			return llmtest.Call("get_ticket", map[string]string{"id": "T-1"}), nil
		}
		return llmtest.Call(spec.SubmitTool, map[string]string{"action": action}), nil
	}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{
		t:       t,
		ctx:     context.Background(),
		store:   storetest.New(t),
		primary: llmtest.NewScripted(),
		shadow:  llmtest.NewScripted(),
		called:  map[string]int{},
	}
	e.primary.Func = answerWith("reply")
	e.shadow.Func = answerWith("reply")

	counted := func(name, result string, t tool.Tool) tool.Tool {
		t.Handler = func(context.Context, json.RawMessage) (string, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.called[name]++
			return result, nil
		}
		return t
	}
	idSchema := json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)
	e.svc = &fleet.Service{
		Store: e.store,
		Runner: &agent.Runner{
			Providers: llm.Registry{"scripted": e.primary, "shadow": e.shadow},
			Pricing:   pricing.Table{"scripted": {"*": {Input: 100, Output: 200}}, "shadow": {"*": {Input: 10, Output: 20}}},
			Recorder:  e.store,
		},
		Tools: &tool.Builder{Builtins: tool.Builtins{
			"get_ticket": counted("get_ticket", `{"subject":"Where is my order?"}`, tool.Tool{Description: "Fetch a ticket", InputSchema: idSchema}),
			"refund":     counted("refund", "refunded", tool.Tool{Description: "Refund an order", InputSchema: idSchema, RequiresApproval: true}),
		}},
		Graders: scoring.Builtin(),
		Rand:    func() float64 { return 0 },
	}
	return e
}

func (e *env) apply(yaml string) *store.Agent {
	e.t.Helper()
	a, _, err := e.svc.Apply(e.ctx, []byte(yaml), "test")
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

func (e *env) emit(expected string) {
	e.t.Helper()
	var exp json.RawMessage
	if expected != "" {
		exp = json.RawMessage(expected)
	}
	if _, err := e.svc.Emit(e.ctx, "ticket.created", json.RawMessage(`{"ticket_id":"T-1"}`), exp); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) drain() int {
	e.t.Helper()
	n, err := e.svc.Drain(e.ctx, "test", 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) runs(f store.RunFilter) []store.Run {
	e.t.Helper()
	runs, err := e.store.Runs(e.ctx, f)
	if err != nil {
		e.t.Fatal(err)
	}
	return runs
}

func (e *env) agent(name string) *store.Agent {
	e.t.Helper()
	a, err := e.store.Agent(e.ctx, name)
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

func (e *env) scores(runID string) map[string]float64 {
	e.t.Helper()
	scores, err := e.store.Scores(e.ctx, runID)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]float64{}
	for _, sc := range scores {
		out[sc.KPI] = sc.Value
	}
	return out
}

func TestEventRunsAndGradesAnAgent(t *testing.T) {
	e := newEnv(t)
	e.apply(triageSpec)

	queued, err := e.svc.Emit(e.ctx, "ticket.created", json.RawMessage(`{"ticket_id":"T-1"}`), json.RawMessage(`{"action":"reply"}`))
	if err != nil || queued != 1 {
		t.Fatalf("Emit = %d, %v", queued, err)
	}
	if n, _ := e.svc.Emit(e.ctx, "order.placed", nil, nil); n != 0 {
		t.Errorf("an event nobody subscribes to queued %d runs", n)
	}
	if worked := e.drain(); worked != 1 {
		t.Fatalf("drained %d jobs, want 1", worked)
	}

	runs := e.runs(store.RunFilter{})
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	run := runs[0]
	if run.Status != store.RunSucceeded || string(run.Output) != `{"action": "reply"}` || run.Trigger != "event:ticket.created" {
		t.Fatalf("run = %s %s trigger=%s error=%q", run.Status, run.Output, run.Trigger, run.Error)
	}
	if run.LLMCalls != 2 || run.ToolCalls != 1 || run.CostUSD <= 0 || run.GradedAt == nil {
		t.Errorf("run totals = %+v", run)
	}
	steps, _ := e.store.Steps(e.ctx, run.ID)
	if len(steps) != 3 {
		t.Errorf("trace has %d steps, want 3", len(steps))
	}

	scores := e.scores(run.ID)
	if scores["accuracy"] != 1 || scores["cost"] != run.CostUSD {
		t.Errorf("scores = %v", scores)
	}
	if _, has := scores["approval_rate"]; has {
		t.Error("approval_rate scored on a run that asked for no approval")
	}

	// The agent never saw the ground truth.
	for _, req := range e.primary.Requests() {
		for _, m := range req.Messages {
			if strings.Contains(m.Text(), "expected") {
				t.Fatalf("ground truth leaked into the conversation: %q", m.Text())
			}
		}
	}
}

func TestReviewPausesAnAgentThatMissesItsTarget(t *testing.T) {
	e := newEnv(t)
	e.apply(triageSpec)
	e.primary.Func = answerWith("escalate") // always wrong

	// Two bad runs are below min_runs: no verdict yet.
	e.emit(`{"action":"reply"}`)
	e.emit(`{"action":"reply"}`)
	e.drain()
	if a := e.agent("triage"); a.Status != store.AgentActive {
		t.Fatalf("status = %s after 2 runs, want active (min_runs is 3)", a.Status)
	}

	e.emit(`{"action":"reply"}`)
	e.drain()
	paused := e.agent("triage")
	if paused.Status != store.AgentPaused {
		t.Fatalf("status = %s after 3 failing runs, want paused", paused.Status)
	}
	if want := "accuracy is 0 over 3 runs (target >= 0.75)"; paused.StatusReason != want {
		t.Errorf("reason = %q, want %q", paused.StatusReason, want)
	}
	events, _ := e.store.AgentEvents(e.ctx, paused.ID, 1)
	if events[0].Kind != store.EventPaused || events[0].Actor != fleet.ActorReview {
		t.Errorf("last event = %+v", events[0])
	}

	// A paused agent takes no new work.
	if n, _ := e.svc.Emit(e.ctx, "ticket.created", nil, nil); n != 0 {
		t.Errorf("event queued %d runs for a paused agent", n)
	}
	if err := e.svc.Trigger(e.ctx, "triage", fleet.TriggerManual, nil, nil); !errors.Is(err, fleet.ErrAgentNotActive) {
		t.Errorf("Trigger on a paused agent: err = %v", err)
	}

	// Resuming restarts the window: one more bad run is not enough to pause again.
	if _, err := e.svc.SetStatus(e.ctx, "triage", store.AgentActive, "prompt fixed", "wojtek"); err != nil {
		t.Fatal(err)
	}
	e.emit(`{"action":"reply"}`)
	e.drain()
	if a := e.agent("triage"); a.Status != store.AgentActive {
		t.Fatalf("status = %s one run after resuming, want active", a.Status)
	}
	card, err := e.svc.Scorecard(e.ctx, e.agent("triage"), mustSpec(t, triageSpec))
	if err != nil || card.Runs != 1 {
		t.Fatalf("scorecard after resume covers %d runs, want 1 (%v)", card.Runs, err)
	}
}

func TestReviewAlertsOnceWhenPolicyIsAlert(t *testing.T) {
	e := newEnv(t)
	a := e.apply(strings.Replace(triageSpec, "min_runs: 3", "min_runs: 2, on_breach: alert", 1))
	e.primary.Func = answerWith("escalate")

	for range 4 {
		e.emit(`{"action":"reply"}`)
	}
	e.drain()

	if got := e.agent("triage"); got.Status != store.AgentActive {
		t.Fatalf("status = %s, want active under the alert policy", got.Status)
	}
	events, _ := e.store.AgentEvents(e.ctx, a.ID, 10)
	breaches := 0
	for _, ev := range events {
		if ev.Kind == store.EventBreach {
			breaches++
		}
	}
	if breaches != 1 {
		t.Errorf("recorded %d breach events for the same breach, want 1", breaches)
	}
}

func TestApprovalFlowThroughTheQueue(t *testing.T) {
	e := newEnv(t)
	e.apply(triageSpec)
	e.primary.Func = func(req *llm.Request) (*llm.Response, error) {
		if len(req.Messages) == 1 {
			return llmtest.Call("refund", map[string]string{"id": "O-9"}), nil
		}
		return llmtest.Call(spec.SubmitTool, map[string]string{"action": "refunded"}), nil
	}

	e.emit("")
	e.drain()
	run := e.runs(store.RunFilter{})[0]
	if run.Status != store.RunAwaitingApproval || run.FinishedAt != nil {
		t.Fatalf("run = %s, want awaiting_approval", run.Status)
	}
	if e.called["refund"] != 0 {
		t.Fatal("refund ran before it was approved")
	}

	pending, _ := e.store.Approvals(e.ctx, store.ApprovalFilter{Status: store.ApprovalPending})
	if len(pending) != 1 || pending[0].Tool != "refund" || string(pending[0].Input) != `{"id": "O-9"}` {
		t.Fatalf("pending = %+v", pending)
	}
	if e.drain() != 0 {
		t.Fatal("the queue had work while the approval was pending")
	}

	if _, err := e.svc.Decide(e.ctx, pending[0].ID, true, "wojtek", "looks right"); err != nil {
		t.Fatal(err)
	}
	if e.drain() != 1 {
		t.Fatal("deciding the approval did not queue the run to continue")
	}

	done, _ := e.store.Run(e.ctx, run.ID)
	if done.Status != store.RunSucceeded || string(done.Output) != `{"action": "refunded"}` {
		t.Fatalf("run = %s %s (%s)", done.Status, done.Output, done.Error)
	}
	if e.called["refund"] != 1 {
		t.Errorf("refund ran %d times, want 1", e.called["refund"])
	}
	if done.LLMCalls != 2 || done.ToolCalls != 1 {
		t.Errorf("totals did not accumulate across the suspension: %+v", done)
	}
	if got := e.scores(run.ID)["approval_rate"]; got != 1 {
		t.Errorf("approval_rate = %v, want 1", got)
	}
}

func TestShadowRunsFeedTheComparison(t *testing.T) {
	e := newEnv(t)
	yaml := triageSpec + "shadow:\n  - {provider: shadow, name: shadow-model}\n"
	a := e.apply(yaml)
	// The primary refunds (a side effect needing approval); the shadow does
	// the same and must not touch anything.
	refundThenSubmit := func(action string) func(*llm.Request) (*llm.Response, error) {
		return func(req *llm.Request) (*llm.Response, error) {
			if len(req.Messages) == 1 {
				return llmtest.Call("get_ticket", map[string]string{"id": "T-1"}), nil
			}
			if len(req.Messages) == 3 {
				return llmtest.Call("refund", map[string]string{"id": "O-9"}), nil
			}
			return llmtest.Call(spec.SubmitTool, map[string]string{"action": action}), nil
		}
	}
	e.primary.Func = answerWith("reply")
	e.shadow.Func = refundThenSubmit("escalate")

	e.emit(`{"action":"reply"}`)
	if worked := e.drain(); worked != 2 {
		t.Fatalf("drained %d jobs, want the run and its shadow", worked)
	}

	primary := e.runs(store.RunFilter{PrimaryOnly: true})[0]
	shadows := e.runs(store.RunFilter{ShadowOf: primary.ID})
	if len(shadows) != 1 {
		t.Fatalf("shadows = %d", len(shadows))
	}
	sh := shadows[0]
	if sh.Status != store.RunSucceeded || sh.Provider != "shadow" || sh.Model != "shadow-model" || string(sh.Input) != string(primary.Input) {
		t.Fatalf("shadow run = %+v", sh)
	}
	if e.called["refund"] != 0 {
		t.Error("a shadow run executed a side effect")
	}
	if e.called["get_ticket"] != 2 {
		t.Errorf("get_ticket ran %d times, want once per run", e.called["get_ticket"])
	}
	if pending, _ := e.store.Approvals(e.ctx, store.ApprovalFilter{}); len(pending) != 0 {
		t.Error("a shadow run asked a person for approval")
	}
	if e.scores(primary.ID)["accuracy"] != 1 || e.scores(sh.ID)["accuracy"] != 0 {
		t.Errorf("accuracy: primary %v, shadow %v", e.scores(primary.ID), e.scores(sh.ID))
	}

	cmp, err := e.svc.Comparison(e.ctx, a, mustSpec(t, yaml))
	if err != nil {
		t.Fatal(err)
	}
	if len(cmp) != 2 || cmp[0].Shadow || cmp[0].Model != "primary-model" || !cmp[1].Shadow || cmp[1].Model != "shadow-model" {
		t.Fatalf("comparison = %+v", cmp)
	}
	if cmp[0].Scorecard.KPIs[0].Value != 1 || cmp[1].Scorecard.KPIs[0].Value != 0 {
		t.Errorf("comparison accuracy: %v vs %v", cmp[0].Scorecard.KPIs[0], cmp[1].Scorecard.KPIs[0])
	}
	if cmp[1].AvgCost >= cmp[0].AvgCost && cmp[1].Runs == cmp[0].Runs {
		// The shadow made one more model call but is ten times cheaper per token.
		t.Errorf("avg cost: primary %v, shadow %v", cmp[0].AvgCost, cmp[1].AvgCost)
	}

	// A bad shadow never gets the agent paused.
	if got := e.agent("triage"); got.Status != store.AgentActive {
		t.Errorf("status = %s", got.Status)
	}
}

func TestSchedules(t *testing.T) {
	e := newEnv(t)
	e.apply(`
name: digest
model: {provider: scripted, name: primary-model}
instructions: Summarise.
triggers:
  - schedule: every 15m
    input: {scope: day}
`)
	e.primary.Func = func(*llm.Request) (*llm.Response, error) { return llmtest.Say("All quiet."), nil }
	e.clock = time.Date(2026, 10, 12, 10, 7, 0, 0, time.UTC)
	e.svc.Now = func() time.Time { return e.clock }

	fire := func() int {
		t.Helper()
		n, err := e.svc.FireSchedules(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	if n := fire(); n != 0 {
		t.Fatalf("first tick queued %d runs, want 0: nothing is overdue on a fresh start", n)
	}
	e.clock = e.clock.Add(5 * time.Minute) // 10:12
	if n := fire(); n != 0 {
		t.Fatalf("queued %d runs before the schedule came due", n)
	}
	e.clock = e.clock.Add(5 * time.Minute) // 10:17, past 10:15
	if n := fire(); n != 1 {
		t.Fatalf("queued %d runs at 10:17, want 1", n)
	}
	if n := fire(); n != 0 {
		t.Fatalf("the same firing time queued %d more runs", n)
	}

	// Two hours of downtime: eight firing times were missed, one run is queued.
	e.clock = e.clock.Add(2 * time.Hour)
	if n := fire(); n != 1 {
		t.Fatalf("after downtime queued %d runs, want 1", n)
	}

	e.drain()
	runs := e.runs(store.RunFilter{})
	if len(runs) != 2 || runs[0].Trigger != fleet.TriggerSchedule || string(runs[0].Input) != `{"scope": "day"}` {
		t.Fatalf("scheduled runs = %d %+v", len(runs), runs)
	}
	if string(runs[0].Output) != `{"text": "All quiet."}` {
		t.Errorf("output = %s", runs[0].Output)
	}
}

func TestInterruptedRunIsFailedNotRepeated(t *testing.T) {
	e := newEnv(t)
	a := e.apply(triageSpec)
	e.emit(`{"action":"reply"}`)

	// A worker claims the job, starts the run, then dies.
	job, err := e.store.Dequeue(e.ctx, "dead-worker")
	if err != nil {
		t.Fatal(err)
	}
	run, err := e.store.CreateRun(e.ctx, store.NewRun{AgentID: a.ID, Trigger: job.Trigger, Input: job.Input, Expected: job.Expected, Provider: a.Provider, Model: a.Model})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.AttachRun(e.ctx, job.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.store.RequeueStale(e.ctx, 0); n != 1 {
		t.Fatalf("requeued %d jobs", n)
	}

	e.drain()
	got, _ := e.store.Run(e.ctx, run.ID)
	if got.Status != store.RunFailed || !strings.HasPrefix(got.Error, "interrupted") {
		t.Fatalf("run = %s %q, want it failed as interrupted", got.Status, got.Error)
	}
	if len(e.runs(store.RunFilter{})) != 1 {
		t.Error("the interrupted run was started a second time")
	}
	if len(e.primary.Requests()) != 0 {
		t.Error("the model was called for a run that should only have been failed")
	}
	// The failure counts against the agent.
	if got.GradedAt == nil || e.scores(run.ID)["accuracy"] != 0 {
		t.Errorf("interrupted run was not graded as a miss: %v", e.scores(run.ID))
	}
}

func TestLateGroundTruthIsGraded(t *testing.T) {
	e := newEnv(t)
	e.apply(triageSpec)
	e.emit("")
	e.drain()

	run := e.runs(store.RunFilter{})[0]
	if _, has := e.scores(run.ID)["accuracy"]; has {
		t.Fatal("accuracy was scored without ground truth")
	}
	labelled, err := e.svc.Label(e.ctx, run.ID, json.RawMessage(`{"action":"escalate"}`))
	if err != nil {
		t.Fatal(err)
	}
	if labelled.GradedAt == nil {
		t.Error("labelled run was not graded again")
	}
	if got, has := e.scores(run.ID)["accuracy"]; !has || got != 0 {
		t.Errorf("accuracy after labelling = %v (present %v), want 0", got, has)
	}
}

func TestTickGradesRunsLeftUngraded(t *testing.T) {
	e := newEnv(t)
	a := e.apply(triageSpec)
	run, _ := e.store.CreateRun(e.ctx, store.NewRun{AgentID: a.ID, Trigger: "manual", Provider: a.Provider, Model: a.Model, Expected: json.RawMessage(`{"action":"reply"}`)})
	if _, err := e.store.SaveOutcome(e.ctx, run, &agent.Outcome{Status: agent.StatusSucceeded, Output: json.RawMessage(`{"action":"reply"}`)}); err != nil {
		t.Fatal(err)
	}

	if err := e.svc.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.scores(run.ID)["accuracy"]; got != 1 {
		t.Errorf("accuracy = %v, want the tick to have graded the run", got)
	}
}

func TestApplyRejectsUnknownGrader(t *testing.T) {
	e := newEnv(t)
	_, _, err := e.svc.Apply(e.ctx, []byte(strings.Replace(triageSpec, "grader: cost", "grader: vibes", 1)), "test")
	if err == nil || !strings.Contains(err.Error(), `unknown grader "vibes"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestToolSetupFailureFailsTheRun(t *testing.T) {
	e := newEnv(t)
	e.apply(strings.Replace(triageSpec, "builtin: get_ticket", "builtin: missing_tool", 1))
	e.emit(`{"action":"reply"}`)
	e.drain()

	run := e.runs(store.RunFilter{})[0]
	if run.Status != store.RunFailed || !strings.Contains(run.Error, `no builtin named "missing_tool"`) {
		t.Fatalf("run = %s %q", run.Status, run.Error)
	}
	if e.scores(run.ID)["accuracy"] != 0 {
		t.Error("a run that could not start was not graded as a miss")
	}
}

func TestRunWorkersProcessJobsUntilCancelled(t *testing.T) {
	e := newEnv(t)
	e.apply(triageSpec)
	for range 6 {
		e.emit(`{"action":"reply"}`)
	}

	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() {
		e.svc.Run(ctx, fleet.WorkerOptions{Concurrency: 3, Poll: 10 * time.Millisecond, TickEvery: 20 * time.Millisecond})
		close(done)
	}()

	deadline := time.Now().Add(20 * time.Second)
	for {
		finished := 0
		for _, r := range e.runs(store.RunFilter{}) {
			if r.Finished() && r.GradedAt != nil {
				finished++
			}
		}
		if finished == 6 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of 6 runs finished", finished)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

func mustSpec(t *testing.T, yaml string) *spec.Spec {
	t.Helper()
	s, err := spec.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
