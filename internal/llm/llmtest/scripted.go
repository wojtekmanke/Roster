// Package llmtest provides a deterministic llm.Provider for tests.
package llmtest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/wojtekmanke/roster/internal/llm"
)

// Scripted replays canned responses in order and records every request it
// receives. When Func is set it answers instead of the script.
type Scripted struct {
	// Func, when non-nil, computes each response from the request.
	Func func(req *llm.Request) (*llm.Response, error)

	mu       sync.Mutex
	turns    []turn
	requests []*llm.Request
}

type turn struct {
	resp *llm.Response
	err  error
}

// NewScripted returns a provider that answers with the given responses in order.
func NewScripted(responses ...*llm.Response) *Scripted {
	s := &Scripted{}
	for _, r := range responses {
		s.turns = append(s.turns, turn{resp: r})
	}
	return s
}

// ThenError queues an error after the responses queued so far.
func (s *Scripted) ThenError(err error) *Scripted {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turns = append(s.turns, turn{err: err})
	return s
}

// Then queues another response.
func (s *Scripted) Then(r *llm.Response) *Scripted {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turns = append(s.turns, turn{resp: r})
	return s
}

// Complete implements llm.Provider.
func (s *Scripted) Complete(_ context.Context, req *llm.Request) (*llm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Copy the message slice: callers keep appending to their own.
	recorded := *req
	recorded.Messages = append([]llm.Message(nil), req.Messages...)
	s.requests = append(s.requests, &recorded)

	if s.Func != nil {
		resp, err := s.Func(&recorded)
		if resp != nil && resp.Model == "" {
			resp.Model = req.Model.Name
		}
		return resp, err
	}
	if len(s.turns) == 0 {
		return nil, fmt.Errorf("llmtest: script exhausted after %d requests", len(s.requests)-1)
	}
	next := s.turns[0]
	s.turns = s.turns[1:]
	if next.err != nil {
		return nil, next.err
	}
	resp := *next.resp
	if resp.Model == "" {
		resp.Model = req.Model.Name
	}
	return &resp, nil
}

// Requests returns the requests received so far.
func (s *Scripted) Requests() []*llm.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*llm.Request(nil), s.requests...)
}

// usage is attached to every helper-built response so cost accounting has
// something to count.
var usage = llm.Usage{InputTokens: 100, OutputTokens: 20}

// Say builds a final text response.
func Say(text string) *llm.Response {
	return &llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{llm.Text(text)}},
		StopReason: llm.StopEndTurn,
		Usage:      usage,
	}
}

// Call builds a response in which the model calls one tool. The call ID is
// derived from the tool name; use Calls for several calls to the same tool.
func Call(tool string, input any) *llm.Response {
	return Calls(llm.ToolUse("call_"+tool, tool, mustJSON(input)))
}

// Calls builds a response containing several tool calls.
func Calls(calls ...llm.Block) *llm.Response {
	return &llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: calls},
		StopReason: llm.StopToolUse,
		Usage:      usage,
	}
}

// Use builds a tool call block with an explicit ID.
func Use(id, tool string, input any) llm.Block {
	return llm.ToolUse(id, tool, mustJSON(input))
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
