// Package openaicompat adapts Chat Completions-style APIs to llm.Provider.
// OpenAI, xAI (Grok) and Ollama all speak this protocol, with small
// differences captured by the constructors below.
package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"

	"github.com/wojtekmanke/roster/internal/llm"
)

// Provider names used in agent specs.
const (
	OpenAI = "openai"
	XAI    = "xai"
	Ollama = "ollama"
)

const (
	xaiBaseURL    = "https://api.x.ai/v1"
	ollamaBaseURL = "http://localhost:11434/v1"
)

// Provider calls one Chat Completions-compatible endpoint.
type Provider struct {
	name   string
	client openai.Client
	// legacyMaxTokens sends the cap as max_tokens rather than
	// max_completion_tokens, for servers that predate the newer field.
	legacyMaxTokens bool
}

// NewOpenAI returns a provider for the OpenAI API. With no options the SDK
// reads OPENAI_API_KEY from the environment.
func NewOpenAI(opts ...option.RequestOption) *Provider {
	return &Provider{name: OpenAI, client: openai.NewClient(opts...)}
}

// NewXAI returns a provider for xAI's Grok models.
func NewXAI(apiKey string, opts ...option.RequestOption) *Provider {
	opts = append([]option.RequestOption{option.WithBaseURL(xaiBaseURL), option.WithAPIKey(apiKey)}, opts...)
	return &Provider{name: XAI, client: openai.NewClient(opts...)}
}

// NewOllama returns a provider for open-source models served by Ollama.
// An empty baseURL means the local default.
func NewOllama(baseURL string, opts ...option.RequestOption) *Provider {
	if baseURL == "" {
		baseURL = ollamaBaseURL
	}
	// Ollama ignores the key, but the SDK insists on sending one.
	opts = append([]option.RequestOption{option.WithBaseURL(baseURL), option.WithAPIKey("ollama")}, opts...)
	return &Provider{name: Ollama, client: openai.NewClient(opts...), legacyMaxTokens: true}
}

// Complete implements llm.Provider.
func (p *Provider) Complete(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	if !req.Model.Fallbacks.IsZero() {
		return nil, fmt.Errorf("%s: refusal fallbacks are not supported by this provider", p.name)
	}
	messages, err := toMessages(req.System, req.Messages)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.name, err)
	}
	tools, err := toTools(req.Tools)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.name, err)
	}

	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(req.Model.Name),
		Messages: messages,
		Tools:    tools,
	}
	if n := int64(req.Model.MaxTokens); n > 0 {
		if p.legacyMaxTokens {
			params.MaxTokens = openai.Int(n)
		} else {
			params.MaxCompletionTokens = openai.Int(n)
		}
	}
	if req.Model.Temperature != nil {
		params.Temperature = openai.Float(*req.Model.Temperature)
	}
	if req.Model.Effort != "" {
		params.ReasoningEffort = shared.ReasoningEffort(req.Model.Effort)
	}

	completion, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, p.wrapError(err)
	}
	if len(completion.Choices) == 0 {
		return nil, &llm.Error{Provider: p.name, Err: errors.New("response contained no choices")}
	}
	return fromCompletion(completion), nil
}

func toMessages(system string, in []llm.Message) ([]openai.ChatCompletionMessageParamUnion, error) {
	var out []openai.ChatCompletionMessageParamUnion
	if system != "" {
		out = append(out, openai.SystemMessage(system))
	}
	for i, m := range in {
		switch m.Role {
		case llm.RoleUser:
			// Tool results are separate "tool" messages in this protocol and
			// must directly follow the assistant turn that made the calls.
			for _, b := range m.Content {
				if b.Type == llm.BlockToolResult {
					out = append(out, openai.ToolMessage(b.Text, b.ToolUseID))
				}
			}
			if text := m.Text(); text != "" {
				out = append(out, openai.UserMessage(text))
			}
		case llm.RoleAssistant:
			var assistant openai.ChatCompletionAssistantMessageParam
			if text := m.Text(); text != "" {
				assistant.Content.OfString = openai.String(text)
			}
			for _, call := range m.ToolUses() {
				assistant.ToolCalls = append(assistant.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
					OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
						ID: call.ToolUseID,
						Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
							Name:      call.ToolName,
							Arguments: string(call.Input),
						},
					},
				})
			}
			out = append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: &assistant})
		default:
			return nil, fmt.Errorf("message %d: unsupported role %q", i, m.Role)
		}
	}
	return out, nil
}

func toTools(defs []llm.ToolDef) ([]openai.ChatCompletionToolUnionParam, error) {
	if len(defs) == 0 {
		return nil, nil
	}
	out := make([]openai.ChatCompletionToolUnionParam, 0, len(defs))
	for _, def := range defs {
		fn := shared.FunctionDefinitionParam{Name: def.Name}
		if def.Description != "" {
			fn.Description = openai.String(def.Description)
		}
		if len(def.InputSchema) > 0 {
			var schema map[string]any
			if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
				return nil, fmt.Errorf("tool %q: input schema is not a JSON object: %w", def.Name, err)
			}
			fn.Parameters = shared.FunctionParameters(schema)
		}
		out = append(out, openai.ChatCompletionFunctionTool(fn))
	}
	return out, nil
}

func fromCompletion(completion *openai.ChatCompletion) *llm.Response {
	choice := completion.Choices[0]
	out := llm.Message{Role: llm.RoleAssistant}
	if choice.Message.Content != "" {
		out.Content = append(out.Content, llm.Text(choice.Message.Content))
	}
	for _, call := range choice.Message.ToolCalls {
		out.Content = append(out.Content, llm.ToolUse(call.ID, call.Function.Name, normalizeArguments(call.Function.Arguments)))
	}

	cached := completion.Usage.PromptTokensDetails.CachedTokens
	resp := &llm.Response{
		Message: out,
		Model:   completion.Model,
		Usage: llm.Usage{
			// prompt_tokens includes cached tokens here; llm.Usage keeps them apart.
			InputTokens:     completion.Usage.PromptTokens - cached,
			OutputTokens:    completion.Usage.CompletionTokens,
			CacheReadTokens: cached,
		},
	}
	switch {
	case choice.Message.Refusal != "" || choice.FinishReason == "content_filter":
		resp.StopReason = llm.StopRefusal
	case len(choice.Message.ToolCalls) > 0:
		// Some servers report "stop" even when the turn carries tool calls.
		resp.StopReason = llm.StopToolUse
	case choice.FinishReason == "length":
		resp.StopReason = llm.StopMaxTokens
	default:
		resp.StopReason = llm.StopEndTurn
	}
	return resp
}

// normalizeArguments turns the model's argument string into JSON. Arguments
// that are not valid JSON are kept as a JSON string so schema validation can
// report the problem back to the model instead of failing the run.
func normalizeArguments(args string) json.RawMessage {
	if args == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(args)) {
		return json.RawMessage(args)
	}
	quoted, _ := json.Marshal(args)
	return quoted
}

func (p *Provider) wrapError(err error) error {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		return &llm.Error{
			Provider:  p.name,
			Status:    apiErr.StatusCode,
			Retryable: llm.RetryableStatus(apiErr.StatusCode),
			Err:       err,
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &llm.Error{Provider: p.name, Err: err}
	}
	return &llm.Error{Provider: p.name, Retryable: true, Err: err}
}
