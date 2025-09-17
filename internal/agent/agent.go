// Package agent runs one agent on one input: it drives the model, executes
// the tools it calls, and stops at a result, a limit, or a call that needs a
// person's approval. It knows nothing about queues or databases.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/pricing"
	"github.com/wojtekmanke/roster/internal/spec"
	"github.com/wojtekmanke/roster/internal/tool"
)

// Status is how an execution segment ended.
type Status string

const (
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	// StatusAwaitingApproval means the run is suspended until every pending
	// approval is decided. Execute it again with the decisions to continue.
	StatusAwaitingApproval Status = "awaiting_approval"
)

// Step kinds.
const (
	StepLLM  = "llm"
	StepTool = "tool"
)

// Step is one recorded action: a model call or a tool call.
type Step struct {
	Seq  int
	Kind string
	// Name is the model that answered, or the tool that was called.
	Name    string
	Input   json.RawMessage
	Output  json.RawMessage
	IsError bool
	// Simulated is true for side effects that a shadow run skipped.
	Simulated bool
	Usage     llm.Usage
	CostUSD   float64
	Latency   time.Duration
	At        time.Time
}

// Recorder receives each step as it happens.
type Recorder interface {
	RecordStep(ctx context.Context, runID string, step Step) error
}

// Totals accumulate over the whole run, across suspensions.
type Totals struct {
	Usage     llm.Usage `json:"usage"`
	CostUSD   float64   `json:"cost_usd"`
	LLMCalls  int       `json:"llm_calls"`
	ToolCalls int       `json:"tool_calls"`
	// ActiveMS is time spent executing, excluding time waiting for approval.
	ActiveMS int64 `json:"active_ms"`
}

// State is everything needed to continue a suspended run. It is persisted as JSON.
type State struct {
	Messages []llm.Message `json:"messages"`
	Pending  *Pending      `json:"pending,omitempty"`
	Totals   Totals        `json:"totals"`
	// Steps is the number of steps recorded so far.
	Steps int `json:"steps"`
	// Traced is how many messages earlier model steps have already shown.
	Traced int `json:"traced"`
	// Reminded is set once the agent has been told to submit a result.
	Reminded bool `json:"reminded,omitempty"`
}

// Pending is an assistant turn whose tool calls are not all answered yet.
type Pending struct {
	// Calls are the turn's tool calls in order.
	Calls []llm.Block `json:"calls"`
	// Results holds the answers already available, keyed by tool call ID.
	Results map[string]llm.Block `json:"results"`
}

// ApprovalRequest asks a person to allow one tool call.
type ApprovalRequest struct {
	ToolUseID string
	Tool      string
	Input     json.RawMessage
}

// Decision is a person's answer to an ApprovalRequest.
type Decision struct {
	Approved bool
	Reason   string
}

// Run is one execution request.
type Run struct {
	ID   string
	Spec *spec.Spec
	// Model is the model to use: the spec's own, or one of its shadows.
	Model llm.ModelConfig
	// Shadow simulates side effects instead of executing them.
	Shadow bool
	Input  json.RawMessage
	// State is nil for a new run, or the state of a suspended one.
	State *State
	// Decisions answers the approvals a suspended run is waiting on, keyed by
	// tool call ID.
	Decisions map[string]Decision
}

// Outcome is the result of an execution segment.
type Outcome struct {
	Status Status
	// Output is the submitted result when Status is StatusSucceeded.
	Output json.RawMessage
	// Error explains a StatusFailed outcome.
	Error string
	// Approvals lists the calls now waiting for a decision.
	Approvals []ApprovalRequest
	State     State
}

// Runner executes runs.
type Runner struct {
	Providers llm.Registry
	Pricing   pricing.Table
	Recorder  Recorder
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// execution is the mutable state of one Execute call.
type execution struct {
	*Runner
	run    Run
	tools  *tool.Set
	state  State
	output *jsonschema.Resolved
}

// Execute runs or resumes a run until it succeeds, fails or suspends.
//
// Problems with the agent itself (a provider error, a blown limit, a refusal)
// produce a StatusFailed outcome. The returned error is reserved for failures
// of the surrounding machinery, such as the recorder.
func (r *Runner) Execute(ctx context.Context, run Run, tools *tool.Set) (*Outcome, error) {
	x := &execution{Runner: r, run: run, tools: tools}
	if run.State != nil {
		x.state = *run.State
	}
	started := r.now()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(run.Spec.Limits.Timeout))
	defer cancel()

	outcome, err := x.execute(ctx)
	if err != nil {
		return nil, err
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && outcome.Status == StatusFailed {
		outcome.Error = fmt.Sprintf("run timed out after %s", time.Duration(run.Spec.Limits.Timeout))
	}
	x.state.Totals.ActiveMS += r.now().Sub(started).Milliseconds()
	outcome.State = x.state
	return outcome, nil
}

func failed(format string, args ...any) *Outcome {
	return &Outcome{Status: StatusFailed, Error: fmt.Sprintf(format, args...)}
}

func (x *execution) execute(ctx context.Context) (*Outcome, error) {
	s := x.run.Spec
	provider, err := x.Providers.Get(x.run.Model.Provider)
	if err != nil {
		return failed("%v", err), nil
	}

	defs := x.tools.Defs()
	if s.Output != nil {
		raw, err := json.Marshal(s.Output.Schema)
		if err != nil {
			return failed("output schema: %v", err), nil
		}
		if x.output, err = tool.CompileSchema(raw); err != nil {
			return failed("output schema: %v", err), nil
		}
		defs = append(defs, llm.ToolDef{
			Name:        spec.SubmitTool,
			Description: "Submit your final result. Call this exactly once, on its own, when the task is finished.",
			InputSchema: raw,
		})
	}

	if len(x.state.Messages) == 0 {
		prompt, err := s.RenderPrompt(x.run.Input)
		if err != nil {
			return failed("%v", err), nil
		}
		x.state.Messages = []llm.Message{llm.UserText(prompt)}
	}

	if x.state.Pending != nil {
		outcome, err := x.resume(ctx)
		if outcome != nil || err != nil {
			return outcome, err
		}
	}

	for {
		if x.state.Totals.LLMCalls >= s.Limits.MaxSteps {
			return failed("step limit reached: %d model calls without a result", s.Limits.MaxSteps), nil
		}
		if s.Limits.MaxCostUSD > 0 && x.state.Totals.CostUSD >= s.Limits.MaxCostUSD {
			return failed("cost limit reached: spent $%.4f of $%.4f", x.state.Totals.CostUSD, s.Limits.MaxCostUSD), nil
		}

		resp, outcome, err := x.callModel(ctx, provider, defs)
		if outcome != nil || err != nil {
			return outcome, err
		}
		switch resp.StopReason {
		case llm.StopRefusal:
			if resp.RefusalCategory != "" {
				return failed("the model declined the request (%s)", resp.RefusalCategory), nil
			}
			return failed("the model declined the request"), nil
		case llm.StopMaxTokens:
			return failed("the model ran out of output tokens mid-turn; raise model.max_tokens"), nil
		}

		calls := resp.Message.ToolUses()
		if len(calls) == 0 {
			if s.Output == nil {
				output, _ := json.Marshal(map[string]string{"text": resp.Message.Text()})
				return &Outcome{Status: StatusSucceeded, Output: output}, nil
			}
			if x.state.Reminded {
				return failed("the agent finished without calling %s", spec.SubmitTool), nil
			}
			x.state.Reminded = true
			x.state.Messages = append(x.state.Messages, llm.UserText(
				"You have not submitted a result. Call "+spec.SubmitTool+" with your final answer."))
			continue
		}

		outcome, err = x.handleCalls(ctx, calls)
		if outcome != nil || err != nil {
			return outcome, err
		}
	}
}

// callModel makes one model call and records it. A non-nil Outcome means the
// call failed and the run with it.
func (x *execution) callModel(ctx context.Context, provider llm.Provider, defs []llm.ToolDef) (*llm.Response, *Outcome, error) {
	req := &llm.Request{
		Model:    x.run.Model,
		System:   x.run.Spec.Instructions,
		Messages: x.state.Messages,
		Tools:    defs,
	}
	// The trace shows only what is new since the previous model call.
	fresh := x.state.Messages[x.state.Traced:]
	input, _ := json.Marshal(map[string]any{"messages": withoutNative(fresh)})

	started := x.now()
	resp, err := provider.Complete(ctx, req)
	step := Step{
		Kind:    StepLLM,
		Name:    x.run.Model.Name,
		Input:   input,
		Latency: x.now().Sub(started),
		At:      started,
	}
	x.state.Totals.LLMCalls++

	if err != nil {
		step.IsError = true
		step.Output, _ = json.Marshal(map[string]string{"error": err.Error()})
		if recErr := x.record(ctx, step); recErr != nil {
			return nil, nil, recErr
		}
		return nil, failed("model call failed: %v", err), nil
	}

	cost, priced := x.Pricing.Cost(x.run.Model.Provider, resp.Model, x.run.Model.Name, resp.Usage)
	x.state.Totals.Usage.Add(resp.Usage)
	x.state.Totals.CostUSD += cost
	x.state.Messages = append(x.state.Messages, resp.Message)
	x.state.Traced = len(x.state.Messages)

	if resp.Model != "" {
		step.Name = resp.Model
	}
	step.Usage = resp.Usage
	step.CostUSD = cost
	output := map[string]any{"content": resp.Message.Content, "stop_reason": resp.StopReason}
	if !priced {
		// Surface missing prices in the trace rather than silently showing $0.
		output["unpriced"] = true
	}
	step.Output, _ = json.Marshal(output)
	if err := x.record(ctx, step); err != nil {
		return nil, nil, err
	}
	return resp, nil, nil
}

// withoutNative drops provider payloads, which duplicate the neutral content.
func withoutNative(msgs []llm.Message) []llm.Message {
	out := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		out[i] = llm.Message{Role: m.Role, Content: m.Content}
	}
	return out
}

func (x *execution) record(ctx context.Context, step Step) error {
	step.Seq = x.state.Steps
	x.state.Steps++
	if x.Recorder == nil {
		return nil
	}
	// A run that has timed out must still be able to write its last step.
	if err := x.Recorder.RecordStep(context.WithoutCancel(ctx), x.run.ID, step); err != nil {
		return fmt.Errorf("agent: record step: %w", err)
	}
	return nil
}
