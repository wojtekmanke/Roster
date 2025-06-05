// Package llm defines the provider-neutral conversation types that the agent
// loop works with. Each provider adapter translates these to and from its own
// wire format.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Role is who authored a message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// BlockType discriminates the content of a Block.
type BlockType string

const (
	BlockText       BlockType = "text"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
)

// Block is one piece of message content.
type Block struct {
	Type BlockType `json:"type"`
	// Text holds the text of a text block, or the content of a tool result.
	Text string `json:"text,omitempty"`
	// ToolUseID identifies a tool call. A tool result carries the ID of the
	// call it answers.
	ToolUseID string          `json:"tool_use_id,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// Text returns a text block.
func Text(s string) Block { return Block{Type: BlockText, Text: s} }

// ToolUse returns a block in which the model calls a tool.
func ToolUse(id, name string, input json.RawMessage) Block {
	return Block{Type: BlockToolUse, ToolUseID: id, ToolName: name, Input: input}
}

// ToolResult returns a block answering the tool call with the given ID.
func ToolResult(id, content string, isError bool) Block {
	return Block{Type: BlockToolResult, ToolUseID: id, Text: content, IsError: isError}
}

// Native is a provider's own encoding of an assistant turn. Adapters replay it
// verbatim when the conversation continues on the same provider, so content
// with no neutral equivalent (such as signed thinking blocks) survives the
// round trip unchanged.
type Native struct {
	Provider string          `json:"provider"`
	Data     json.RawMessage `json:"data"`
}

// Message is one turn of a conversation.
type Message struct {
	Role    Role    `json:"role"`
	Content []Block `json:"content"`
	Native  *Native `json:"native,omitempty"`
}

// UserText returns a user message containing a single text block.
func UserText(s string) Message {
	return Message{Role: RoleUser, Content: []Block{Text(s)}}
}

// Text returns the message's text blocks joined by newlines.
func (m Message) Text() string {
	var parts []string
	for _, b := range m.Content {
		if b.Type == BlockText && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// ToolUses returns the tool calls in the message, in order.
func (m Message) ToolUses() []Block {
	var calls []Block
	for _, b := range m.Content {
		if b.Type == BlockToolUse {
			calls = append(calls, b)
		}
	}
	return calls
}

// ToolDef describes a tool the model may call.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Fallbacks selects models that should answer when the requested model
// declines a request. It is either the provider's recommended routing
// ("default" in YAML) or an explicit list of model names.
type Fallbacks struct {
	Default bool
	Models  []string
}

// IsZero reports whether no fallback is configured.
func (f Fallbacks) IsZero() bool { return !f.Default && len(f.Models) == 0 }

// UnmarshalYAML accepts the scalar "default" or a list of model names.
func (f *Fallbacks) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Value != "default" {
			return fmt.Errorf("line %d: fallbacks must be \"default\" or a list of model names", node.Line)
		}
		f.Default = true
		return nil
	case yaml.SequenceNode:
		return node.Decode(&f.Models)
	}
	return fmt.Errorf("line %d: fallbacks must be \"default\" or a list of model names", node.Line)
}

// MarshalYAML is the inverse of UnmarshalYAML.
func (f Fallbacks) MarshalYAML() (any, error) {
	if f.Default {
		return "default", nil
	}
	return f.Models, nil
}

// ModelConfig selects a model and its sampling settings. It is the `model`
// section of an agent spec.
type ModelConfig struct {
	Provider string `yaml:"provider" json:"provider"`
	Name     string `yaml:"name" json:"name"`
	// MaxTokens caps a single response. Zero means the adapter's default.
	MaxTokens int `yaml:"max_tokens,omitempty" json:"max_tokens,omitempty"`
	// Temperature is sent only when set. Several current models reject it.
	Temperature *float64 `yaml:"temperature,omitempty" json:"temperature,omitempty"`
	// Effort is the reasoning effort (low, medium, high, ...) for models that
	// accept one. Empty means the provider's default.
	Effort    string    `yaml:"effort,omitempty" json:"effort,omitempty"`
	Fallbacks Fallbacks `yaml:"fallbacks,omitempty" json:"-"`
}

// String returns "provider/name".
func (m ModelConfig) String() string { return m.Provider + "/" + m.Name }

// Request is one model turn: the full conversation so far plus the tools on offer.
type Request struct {
	Model    ModelConfig
	System   string
	Messages []Message
	Tools    []ToolDef
}

// Usage counts tokens for one or more model calls.
type Usage struct {
	// InputTokens excludes tokens read from or written to a prompt cache.
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
}

// Add accumulates o into u.
func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheReadTokens += o.CacheReadTokens
	u.CacheWriteTokens += o.CacheWriteTokens
}

// StopReason says why the model stopped generating.
type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	// StopRefusal means the model or a safety classifier declined the request.
	StopRefusal StopReason = "refusal"
)

// Response is the model's reply to a Request.
type Response struct {
	Message    Message
	StopReason StopReason
	Usage      Usage
	// Model is the model that actually answered. It differs from the requested
	// model when a fallback served the request.
	Model string
	// RefusalCategory is set by some providers when StopReason is StopRefusal.
	RefusalCategory string
}

// Provider completes one model turn.
type Provider interface {
	Complete(ctx context.Context, req *Request) (*Response, error)
}

// Error is a failed provider call.
type Error struct {
	Provider string
	// Status is the HTTP status, or 0 when the request never got a response.
	Status int
	// Retryable is true for rate limits, overload and transport failures.
	Retryable bool
	Err       error
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s: HTTP %d: %v", e.Provider, e.Status, e.Err)
	}
	return fmt.Sprintf("%s: %v", e.Provider, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// RetryableStatus reports whether an HTTP status is worth retrying.
func RetryableStatus(status int) bool {
	return status == 408 || status == 409 || status == 429 || status >= 500
}

// Registry maps provider names to adapters.
type Registry map[string]Provider

// Get returns the named provider.
func (r Registry) Get(name string) (Provider, error) {
	p, ok := r[name]
	if !ok {
		return nil, fmt.Errorf("llm: provider %q is not configured (have: %s)", name, strings.Join(r.Names(), ", "))
	}
	return p, nil
}

// Names returns the configured provider names, sorted.
func (r Registry) Names() []string {
	names := make([]string, 0, len(r))
	for name := range r {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
