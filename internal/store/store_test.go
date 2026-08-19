package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wojtekmanke/roster/internal/agent"
	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/store"
	"github.com/wojtekmanke/roster/internal/store/storetest"
)

func agentYAML(name, extra string) []byte {
	return []byte(fmt.Sprintf("name: %s\ndescription: Test agent.\nmodel: {provider: scripted, name: m1}\ninstructions: Do it.\n%s", name, extra))
}

func mustApply(t *testing.T, s *store.Store, name string) *store.Agent {
	t.Helper()
	a, _, err := s.ApplyAgent(context.Background(), agentYAML(name, ""), "test")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustRun(t *testing.T, s *store.Store, a *store.Agent) *store.Run {
	t.Helper()
	run, err := s.CreateRun(context.Background(), store.NewRun{
		AgentID: a.ID, Trigger: "manual", Input: json.RawMessage(`{"n":1}`), Provider: a.Provider, Model: a.Model,
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func eventKinds(t *testing.T, s *store.Store, agentID string) string {
	t.Helper()
	events, err := s.AgentEvents(context.Background(), agentID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for i := len(events) - 1; i >= 0; i-- {
		kinds = append(kinds, events[i].Kind)
	}
	return strings.Join(kinds, ",")
}

func TestApplyAgent(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()

	created, changed, err := s.ApplyAgent(ctx, agentYAML("triage", ""), "cli")
	if err != nil || !changed {
		t.Fatalf("create: changed=%v err=%v", changed, err)
	}
	if created.Status != store.AgentActive || created.Provider != "scripted" || created.Model != "m1" || !strings.HasPrefix(created.ID, "agt_") {
		t.Errorf("created = %+v", created)
	}

	same, changed, err := s.ApplyAgent(ctx, agentYAML("triage", ""), "cli")
	if err != nil || changed || same.ID != created.ID {
		t.Fatalf("re-apply identical spec: changed=%v err=%v", changed, err)
	}

	updated, changed, err := s.ApplyAgent(ctx, agentYAML("triage", "limits: {max_steps: 3}\n"), "cli")
	if err != nil || !changed || updated.ID != created.ID || updated.SpecHash == created.SpecHash {
		t.Fatalf("update: changed=%v err=%v", changed, err)
	}
	parsed, err := updated.Spec()
	if err != nil || parsed.Limits.MaxSteps != 3 {
		t.Errorf("stored spec did not round-trip: %v", err)
	}
	if got := eventKinds(t, s, created.ID); got != "created,spec_updated" {
		t.Errorf("events = %s", got)
	}

	if _, _, err := s.ApplyAgent(ctx, []byte("name: Bad Name\n"), "cli"); err == nil {
		t.Error("an invalid spec was stored")
	}
	if _, err := s.Agent(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if agents, err := s.Agents(ctx); err != nil || len(agents) != 1 {
		t.Errorf("Agents() = %d, %v", len(agents), err)
	}
}

func TestAgentStatusTransitions(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	a := mustApply(t, s, "triage")

	paused, err := s.SetAgentStatus(ctx, a.ID, store.AgentPaused, "accuracy 0.4 < 0.8", "review")
	if err != nil || paused.Status != store.AgentPaused || paused.StatusReason != "accuracy 0.4 < 0.8" {
		t.Fatalf("pause: %+v %v", paused, err)
	}
	if !paused.ReviewSince.Equal(a.ReviewSince) {
		t.Error("pausing moved the review window")
	}
	// Pausing twice is a no-op, not a second event.
	if _, err := s.SetAgentStatus(ctx, a.ID, store.AgentPaused, "again", "review"); err != nil {
		t.Fatal(err)
	}

	resumed, err := s.SetAgentStatus(ctx, a.ID, store.AgentActive, "fixed the prompt", "wojtek")
	if err != nil || resumed.Status != store.AgentActive {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	if !resumed.ReviewSince.After(a.ReviewSince) {
		t.Error("resuming did not restart the review window")
	}

	// Retiring cancels queued work and is final.
	if _, err := s.Enqueue(ctx, store.NewJob{AgentID: a.ID, Kind: store.JobRun, Trigger: "manual"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAgentStatus(ctx, a.ID, store.AgentRetired, "replaced", "wojtek"); err != nil {
		t.Fatal(err)
	}
	if depth, _ := s.QueueDepth(ctx); depth != 0 {
		t.Errorf("queue depth = %d after retiring, want 0", depth)
	}
	if _, err := s.SetAgentStatus(ctx, a.ID, store.AgentActive, "", "wojtek"); err == nil {
		t.Error("a retired agent was reactivated")
	}
	if got := eventKinds(t, s, a.ID); got != "created,paused,resumed,retired" {
		t.Errorf("events = %s", got)
	}
}

func TestRunLifecycleWithApproval(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	a := mustApply(t, s, "refunder")
	run := mustRun(t, s, a)

	if run.Status != store.RunRunning || run.Output != nil || run.FinishedAt != nil || run.IsShadow() {
		t.Fatalf("new run = %+v", run)
	}

	for i, step := range []agent.Step{
		{Seq: 0, Kind: agent.StepLLM, Name: "m1", Input: json.RawMessage(`{"messages":[]}`), Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}, CostUSD: 0.01, Latency: 120 * time.Millisecond, At: time.Now()},
		{Seq: 1, Kind: agent.StepTool, Name: "get_order", Output: json.RawMessage(`"ok"`), Simulated: true, At: time.Now()},
	} {
		if err := s.RecordStep(ctx, run.ID, step); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}

	// The run suspends on two approvals.
	suspended := &agent.Outcome{
		Status: agent.StatusAwaitingApproval,
		Approvals: []agent.ApprovalRequest{
			{ToolUseID: "c1", Tool: "refund_order", Input: json.RawMessage(`{"id":"O-1"}`)},
			{ToolUseID: "c2", Tool: "refund_order", Input: json.RawMessage(`{"id":"O-2"}`)},
		},
		State: agent.State{
			Messages: []llm.Message{llm.UserText("go")},
			Totals:   agent.Totals{Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}, CostUSD: 0.01, LLMCalls: 1, ToolCalls: 1, ActiveMS: 150},
		},
	}
	saved, err := s.SaveOutcome(ctx, run, suspended)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != store.RunAwaitingApproval || saved.FinishedAt != nil || saved.State == nil || saved.CostUSD != 0.01 || saved.LLMCalls != 1 {
		t.Fatalf("suspended run = %+v", saved)
	}
	var state agent.State
	if err := json.Unmarshal(saved.State, &state); err != nil || len(state.Messages) != 1 {
		t.Fatalf("state did not round-trip: %v", err)
	}

	pending, err := s.Approvals(ctx, store.ApprovalFilter{Status: store.ApprovalPending})
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending approvals = %d, %v", len(pending), err)
	}
	// Both rows share a transaction timestamp, so their order is arbitrary.
	if pending[0].ToolUseID != "c1" {
		pending[0], pending[1] = pending[1], pending[0]
	}

	// Saving the same suspension again must not duplicate the approvals.
	if _, err := s.SaveOutcome(ctx, run, suspended); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Approvals(ctx, store.ApprovalFilter{RunID: run.ID}); len(again) != 2 {
		t.Fatalf("approvals after a second save = %d, want 2", len(again))
	}

	// Deciding the first approval does not resume the run yet.
	first, err := s.DecideApproval(ctx, pending[0].ID, true, "wojtek", "")
	if err != nil || first.Status != store.ApprovalApproved || first.DecidedAt == nil {
		t.Fatalf("first decision = %+v, %v", first, err)
	}
	if depth, _ := s.QueueDepth(ctx); depth != 0 {
		t.Fatalf("queue depth = %d with an approval still pending, want 0", depth)
	}
	if _, err := s.DecideApproval(ctx, pending[0].ID, false, "wojtek", ""); err == nil || !strings.Contains(err.Error(), "already been decided") {
		t.Errorf("deciding twice: err = %v", err)
	}
	if _, err := s.DecideApproval(ctx, "apr_missing", true, "wojtek", ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown approval: err = %v", err)
	}

	// The last decision queues the resume job.
	if _, err := s.DecideApproval(ctx, pending[1].ID, false, "wojtek", "duplicate refund"); err != nil {
		t.Fatal(err)
	}
	job, err := s.Dequeue(ctx, "w1")
	if err != nil || job.Kind != store.JobResume || job.RunID == nil || *job.RunID != run.ID {
		t.Fatalf("resume job = %+v, %v", job, err)
	}
	decisions, err := s.Decisions(ctx, run.ID)
	if err != nil || !decisions["c1"].Approved || decisions["c2"].Approved || decisions["c2"].Reason != "duplicate refund" {
		t.Fatalf("decisions = %+v, %v", decisions, err)
	}

	// The run finishes.
	done := &agent.Outcome{Status: agent.StatusSucceeded, Output: json.RawMessage(`{"answer":"ok"}`), State: suspended.State}
	finished, err := s.SaveOutcome(ctx, run, done)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != store.RunSucceeded || finished.FinishedAt == nil || finished.State != nil || string(finished.Output) != `{"answer": "ok"}` {
		t.Fatalf("finished run = %+v (output %s)", finished, finished.Output)
	}

	steps, err := s.Steps(ctx, run.ID)
	if err != nil || len(steps) != 2 || steps[0].LatencyMS != 120 || !steps[1].Simulated || steps[1].Input != nil {
		t.Fatalf("steps = %+v, %v", steps, err)
	}
}

func TestRunsFilter(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	a := mustApply(t, s, "one")
	b := mustApply(t, s, "two")

	primary := mustRun(t, s, a)
	mustRun(t, s, b)
	shadow, err := s.CreateRun(ctx, store.NewRun{AgentID: a.ID, Trigger: "manual", Provider: "ollama", Model: "llama", ShadowOf: &primary.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FailRun(ctx, shadow.ID, "worker lost"); err != nil {
		t.Fatal(err)
	}

	count := func(f store.RunFilter) int {
		runs, err := s.Runs(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(runs)
	}
	if got := count(store.RunFilter{}); got != 3 {
		t.Errorf("all = %d", got)
	}
	if got := count(store.RunFilter{AgentID: a.ID}); got != 2 {
		t.Errorf("agent a = %d", got)
	}
	if got := count(store.RunFilter{AgentID: a.ID, PrimaryOnly: true}); got != 1 {
		t.Errorf("agent a primary = %d", got)
	}
	if got := count(store.RunFilter{ShadowOf: primary.ID}); got != 1 {
		t.Errorf("shadows = %d", got)
	}
	if got := count(store.RunFilter{Status: store.RunFailed}); got != 1 {
		t.Errorf("failed = %d", got)
	}

	failed, _ := s.Run(ctx, shadow.ID)
	if failed.Error != "worker lost" || !failed.Finished() || !failed.IsShadow() {
		t.Errorf("failed shadow = %+v", failed)
	}
}

func TestQueue(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	a := mustApply(t, s, "worker")
	b := mustApply(t, s, "sleeper")

	if _, err := s.Dequeue(ctx, "w"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("empty queue: err = %v", err)
	}

	// Dedupe keys make scheduled enqueues idempotent.
	for i, want := range []bool{true, false} {
		got, err := s.Enqueue(ctx, store.NewJob{AgentID: a.ID, Kind: store.JobRun, Trigger: "schedule", DedupeKey: "sched:1"})
		if err != nil || got != want {
			t.Fatalf("enqueue %d: got %v, %v; want %v", i, got, err, want)
		}
	}
	if _, err := s.Enqueue(ctx, store.NewJob{AgentID: b.ID, Kind: store.JobRun, Trigger: "manual", Input: json.RawMessage(`{"x":1}`), Expected: json.RawMessage(`{"y":2}`)}); err != nil {
		t.Fatal(err)
	}

	// A paused agent's jobs wait.
	if _, err := s.SetAgentStatus(ctx, a.ID, store.AgentPaused, "test", "test"); err != nil {
		t.Fatal(err)
	}
	job, err := s.Dequeue(ctx, "w")
	if err != nil || job.AgentID != b.ID || job.Attempts != 1 || string(job.Expected) != `{"y": 2}` {
		t.Fatalf("job = %+v, %v; want the active agent's job", job, err)
	}
	if _, err := s.Dequeue(ctx, "w"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a paused agent's job was handed out")
	}
	if _, err := s.SetAgentStatus(ctx, a.ID, store.AgentActive, "", "test"); err != nil {
		t.Fatal(err)
	}
	if job, err := s.Dequeue(ctx, "w"); err != nil || job.AgentID != a.ID {
		t.Fatalf("after resume: %+v, %v", job, err)
	}

	// A job whose worker died goes back on the queue with its attempt counted.
	if n, err := s.RequeueStale(ctx, time.Hour); err != nil || n != 0 {
		t.Fatalf("RequeueStale(1h) = %d, %v; nothing is that old", n, err)
	}
	if n, err := s.RequeueStale(ctx, 0); err != nil || n != 2 {
		t.Fatalf("RequeueStale(0) = %d, %v; want both claimed jobs", n, err)
	}
	again, err := s.Dequeue(ctx, "w2")
	if err != nil || again.Attempts != 2 {
		t.Fatalf("redelivered job = %+v, %v", again, err)
	}
	if err := s.FinishJob(ctx, again.ID, "boom"); err != nil {
		t.Fatal(err)
	}
	if depth, _ := s.QueueDepth(ctx); depth != 1 {
		t.Errorf("queue depth = %d, want 1", depth)
	}
}

func TestRunAtSetsQueuePosition(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	a := mustApply(t, s, "worker")

	for _, trigger := range []string{"first", "second", "third"} {
		if _, err := s.Enqueue(ctx, store.NewJob{AgentID: a.ID, Kind: store.JobRun, Trigger: trigger}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.Dequeue(ctx, "w")
	if err != nil || first.Trigger != "first" {
		t.Fatalf("first = %+v, %v", first, err)
	}
	// A job queued now, but with the first job's place, runs before the backlog.
	if _, err := s.Enqueue(ctx, store.NewJob{AgentID: a.ID, Kind: store.JobRun, Trigger: "shadow of first", RunAt: first.RunAt}); err != nil {
		t.Fatal(err)
	}
	var order []string
	for {
		job, err := s.Dequeue(ctx, "w")
		if errors.Is(err, store.ErrNotFound) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		order = append(order, job.Trigger)
	}
	if got := strings.Join(order, ", "); got != "shadow of first, second, third" {
		t.Fatalf("order = %s", got)
	}
}

func TestDequeueNeverHandsOutAJobTwice(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	a := mustApply(t, s, "worker")

	const jobs, workers = 40, 8
	for range jobs {
		if _, err := s.Enqueue(ctx, store.NewJob{AgentID: a.ID, Kind: store.JobRun, Trigger: "manual"}); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				job, err := s.Dequeue(ctx, fmt.Sprintf("w%d", w))
				if errors.Is(err, store.ErrNotFound) {
					return
				}
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				seen[job.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(seen) != jobs {
		t.Fatalf("claimed %d distinct jobs, want %d", len(seen), jobs)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("job %d was claimed %d times", id, n)
		}
	}
}

func TestScoresAndWindows(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	a := mustApply(t, s, "graded")

	finish := func(n NewRunOpt) *store.Run {
		t.Helper()
		run, err := s.CreateRun(ctx, store.NewRun{AgentID: a.ID, Trigger: "manual", Provider: n.provider, Model: n.model, ShadowOf: n.shadowOf})
		if err != nil {
			t.Fatal(err)
		}
		done, err := s.SaveOutcome(ctx, run, &agent.Outcome{Status: agent.StatusSucceeded, State: agent.State{Totals: agent.Totals{CostUSD: n.cost}}})
		if err != nil {
			t.Fatal(err)
		}
		return done
	}

	var primaries []*store.Run
	for i := range 4 {
		run := finish(NewRunOpt{provider: "scripted", model: "m1", cost: 0.5})
		primaries = append(primaries, run)
		err := s.SaveScores(ctx, run, []store.Score{
			{KPI: "accuracy", Grader: "expected", Metric: "match", Value: float64(i % 2)},
			{KPI: "cost", Grader: "cost", Metric: "usd", Value: 0.5, Detail: json.RawMessage(`{"note":"x"}`)},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	shadow := finish(NewRunOpt{provider: "ollama", model: "llama", shadowOf: &primaries[0].ID})
	if err := s.SaveScores(ctx, shadow, []store.Score{{KPI: "accuracy", Grader: "expected", Metric: "match", Value: 1}}); err != nil {
		t.Fatal(err)
	}
	ungraded := finish(NewRunOpt{provider: "scripted", model: "m1"})

	pending, err := s.UngradedRuns(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].ID != ungraded.ID {
		t.Fatalf("ungraded = %+v, %v", pending, err)
	}

	window := store.ScoreWindow{AgentID: a.ID, Since: a.ReviewSince, Limit: 3, Provider: "scripted", Model: "m1"}
	recent, err := s.ScoredRuns(ctx, window)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 3 {
		t.Fatalf("window returned %d runs, want the 3 most recent", len(recent))
	}
	if recent[0].RunID != primaries[3].ID || recent[2].RunID != primaries[1].ID {
		t.Errorf("window is not newest-first: %s ... %s", recent[0].RunID, recent[2].RunID)
	}
	if got := recent[0].Scores["accuracy"]; got != 1 {
		t.Errorf("newest run accuracy = %v, want 1", got)
	}
	if len(recent[0].Scores) != 2 {
		t.Errorf("scores = %v", recent[0].Scores)
	}

	window.Provider, window.Model, window.Shadow = "ollama", "llama", true
	if shadows, err := s.ScoredRuns(ctx, window); err != nil || len(shadows) != 1 {
		t.Fatalf("shadow window = %d, %v", len(shadows), err)
	}
	window.Since = time.Now().Add(time.Hour)
	if none, err := s.ScoredRuns(ctx, window); err != nil || len(none) != 0 {
		t.Fatalf("window after Since = %d, %v", len(none), err)
	}

	// Regrading replaces scores rather than adding to them.
	if err := s.SaveScores(ctx, primaries[0], []store.Score{{KPI: "accuracy", Grader: "expected", Metric: "match", Value: 1}}); err != nil {
		t.Fatal(err)
	}
	scores, err := s.Scores(ctx, primaries[0].ID)
	if err != nil || len(scores) != 1 || scores[0].Value != 1 {
		t.Fatalf("scores after regrade = %+v, %v", scores, err)
	}

	// Late ground truth clears the grade.
	labelled, err := s.SetExpected(ctx, primaries[1].ID, json.RawMessage(`{"answer":"x"}`))
	if err != nil || labelled.GradedAt != nil || string(labelled.Expected) != `{"answer": "x"}` {
		t.Fatalf("labelled = %+v, %v", labelled, err)
	}

	models, err := s.ModelsUsed(ctx, a.ID, a.ReviewSince)
	if err != nil || len(models) != 2 || models[0].Shadow || models[0].Runs != 5 || !models[1].Shadow {
		t.Fatalf("models = %+v, %v", models, err)
	}

	stats, err := s.StatsSince(ctx, a.ReviewSince)
	if err != nil {
		t.Fatal(err)
	}
	if st := stats[a.ID]; st.Runs != 5 || st.Succeeded != 5 || st.CostUSD != 2 || st.LastRunAt == nil {
		t.Errorf("stats = %+v (shadow runs must not count)", st)
	}
}

type NewRunOpt struct {
	provider, model string
	shadowOf        *string
	cost            float64
}

func TestSchedulerTick(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)

	first, err := s.SchedulerTick(ctx, t0)
	if err != nil || !first.Equal(t0) {
		t.Fatalf("first tick = %v, %v; want now", first, err)
	}
	second, err := s.SchedulerTick(ctx, t0.Add(time.Minute))
	if err != nil || !second.Equal(t0) {
		t.Fatalf("second tick = %v, %v; want the previous tick", second, err)
	}
	// A clock that goes backwards must not move the watermark back.
	if _, err := s.SchedulerTick(ctx, t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	fourth, err := s.SchedulerTick(ctx, t0.Add(2*time.Minute))
	if err != nil || !fourth.Equal(t0.Add(time.Minute)) {
		t.Fatalf("fourth tick = %v, %v; want t0+1m", fourth, err)
	}
}
