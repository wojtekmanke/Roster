package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/spec"
	"github.com/wojtekmanke/roster/internal/tool"
)

// simulatedResult is what a shadow run tells the model in place of a side effect.
const simulatedResult = `{"simulated":true,"note":"This is a dry run: the action was not carried out. Continue as if it had succeeded."}`

// handleCalls answers the tool calls of the latest assistant turn. It returns
// a non-nil Outcome when the run ends or suspends, and nil to keep looping.
func (x *execution) handleCalls(ctx context.Context, calls []llm.Block) (*Outcome, error) {
	results := map[string]llm.Block{}
	var approvals []ApprovalRequest
	var submitted json.RawMessage

	for _, call := range calls {
		if call.ToolName == spec.SubmitTool {
			result, accepted := x.checkSubmission(call, len(calls))
			results[call.ToolUseID] = result
			if accepted {
				submitted = call.Input
			}
			continue
		}

		t, problem := x.lookup(call)
		switch {
		case problem != "":
			results[call.ToolUseID] = llm.ToolResult(call.ToolUseID, problem, true)
		case x.run.Shadow && t.SideEffect:
			if err := x.recordSimulated(ctx, call); err != nil {
				return nil, err
			}
			results[call.ToolUseID] = llm.ToolResult(call.ToolUseID, simulatedResult, false)
		case t.RequiresApproval:
			approvals = append(approvals, ApprovalRequest{ToolUseID: call.ToolUseID, Tool: call.ToolName, Input: call.Input})
		default:
			result, err := x.runTool(ctx, t, call)
			if err != nil {
				return nil, err
			}
			results[call.ToolUseID] = result
		}
	}

	if submitted != nil {
		return &Outcome{Status: StatusSucceeded, Output: submitted}, nil
	}
	if len(approvals) > 0 {
		// Keep the answers we already have; the turn is completed on resume.
		x.state.Pending = &Pending{Calls: calls, Results: results}
		return &Outcome{Status: StatusAwaitingApproval, Approvals: approvals}, nil
	}
	x.answer(calls, results)
	return nil, nil
}

// resume completes a pending turn using the decisions on the run. It returns
// a non-nil Outcome if approvals are still outstanding.
func (x *execution) resume(ctx context.Context) (*Outcome, error) {
	pending := x.state.Pending
	if pending.Results == nil {
		pending.Results = map[string]llm.Block{}
	}

	var waiting []ApprovalRequest
	for _, call := range pending.Calls {
		if _, answered := pending.Results[call.ToolUseID]; answered {
			continue
		}
		decision, decided := x.run.Decisions[call.ToolUseID]
		if !decided {
			waiting = append(waiting, ApprovalRequest{ToolUseID: call.ToolUseID, Tool: call.ToolName, Input: call.Input})
			continue
		}
		if !decision.Approved {
			msg := "A reviewer rejected this call"
			if decision.Reason != "" {
				msg += ": " + decision.Reason
			}
			pending.Results[call.ToolUseID] = llm.ToolResult(call.ToolUseID, msg+". Do not retry it.", true)
			continue
		}
		t, problem := x.lookup(call)
		if problem != "" {
			pending.Results[call.ToolUseID] = llm.ToolResult(call.ToolUseID, problem, true)
			continue
		}
		result, err := x.runTool(ctx, t, call)
		if err != nil {
			return nil, err
		}
		pending.Results[call.ToolUseID] = result
	}

	if len(waiting) > 0 {
		return &Outcome{Status: StatusAwaitingApproval, Approvals: waiting}, nil
	}
	x.answer(pending.Calls, pending.Results)
	x.state.Pending = nil
	return nil, nil
}

// answer appends one user message holding every result, in call order.
// Providers expect all results of a turn together; splitting them teaches
// the model to stop making parallel calls.
func (x *execution) answer(calls []llm.Block, results map[string]llm.Block) {
	blocks := make([]llm.Block, 0, len(calls))
	for _, call := range calls {
		blocks = append(blocks, results[call.ToolUseID])
	}
	x.state.Messages = append(x.state.Messages, llm.Message{Role: llm.RoleUser, Content: blocks})
}

// checkSubmission validates a submit_result call.
func (x *execution) checkSubmission(call llm.Block, callsInTurn int) (llm.Block, bool) {
	if x.output == nil {
		return llm.ToolResult(call.ToolUseID, "Error: this agent has no "+spec.SubmitTool+" tool; reply in plain text instead.", true), false
	}
	if callsInTurn > 1 {
		return llm.ToolResult(call.ToolUseID, "Error: "+spec.SubmitTool+" must be called on its own, after every other tool call has returned.", true), false
	}
	if err := tool.ValidateJSON(x.output, call.Input); err != nil {
		return llm.ToolResult(call.ToolUseID, "Error: the result does not match the required schema: "+err.Error(), true), false
	}
	return llm.ToolResult(call.ToolUseID, "Result accepted.", false), true
}

// lookup finds the tool for a call and validates its input. The string is a
// message for the model when the call cannot be made.
func (x *execution) lookup(call llm.Block) (*tool.Tool, string) {
	t, ok := x.tools.Get(call.ToolName)
	if !ok {
		return nil, fmt.Sprintf("Error: there is no tool named %q.", call.ToolName)
	}
	if err := t.Validate(call.Input); err != nil {
		return nil, "Error: invalid input: " + err.Error()
	}
	return t, ""
}

// runTool executes a tool and records the step. Tool failures become error
// results for the model; only recorder failures are returned as errors.
func (x *execution) runTool(ctx context.Context, t *tool.Tool, call llm.Block) (llm.Block, error) {
	timeout := time.Duration(x.run.Spec.Limits.ToolTimeout)
	toolCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	started := x.now()
	out, err := t.Handler(toolCtx, call.Input)
	step := Step{
		Kind:    StepTool,
		Name:    call.ToolName,
		Input:   call.Input,
		Latency: x.now().Sub(started),
		At:      started,
	}
	x.state.Totals.ToolCalls++

	result := llm.ToolResult(call.ToolUseID, out, false)
	if err != nil {
		msg := err.Error()
		if errors.Is(toolCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			msg = fmt.Sprintf("the tool did not answer within %s", timeout)
		}
		result = llm.ToolResult(call.ToolUseID, "Error: "+msg, true)
		step.IsError = true
	}
	step.Output = textOutput(result.Text)
	if err := x.record(ctx, step); err != nil {
		return llm.Block{}, err
	}
	return result, nil
}

func (x *execution) recordSimulated(ctx context.Context, call llm.Block) error {
	x.state.Totals.ToolCalls++
	return x.record(ctx, Step{
		Kind:      StepTool,
		Name:      call.ToolName,
		Input:     call.Input,
		Output:    json.RawMessage(simulatedResult),
		Simulated: true,
		At:        x.now(),
	})
}

// textOutput stores a tool result as JSON: verbatim when it already is JSON,
// otherwise as a JSON string.
func textOutput(text string) json.RawMessage {
	if json.Valid([]byte(text)) {
		return json.RawMessage(text)
	}
	quoted, _ := json.Marshal(text)
	return quoted
}
