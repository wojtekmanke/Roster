// Package anthropic adapts the Claude Messages API to llm.Provider.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	"github.com/wojtekmanke/roster/internal/llm"
)

// Name is the provider name used in agent specs.
const Name = "anthropic"

// defaultMaxTokens leaves the model room to finish without risking the SDK's
// non-streaming HTTP timeout.
const defaultMaxTokens = 16000

// Provider calls Claude through the official SDK.
type Provider struct {
	client sdk.Client
}

// New returns a provider. With no options the SDK resolves credentials from
// the environment (ANTHROPIC_API_KEY and friends).
func New(opts ...option.RequestOption) *Provider {
	return &Provider{client: sdk.NewClient(opts...)}
}

// Complete implements llm.Provider.
func (p *Provider) Complete(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	messages, err := toMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	tools, err := toTools(req.Tools)
	if err != nil {
		return nil, err
	}

	maxTokens := req.Model.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultMaxTokens
	}
	params := sdk.BetaMessageNewParams{
		Model:     sdk.Model(req.Model.Name),
		MaxTokens: int64(maxTokens),
		Messages:  messages,
		Tools:     tools,
		// An agent loop resends a growing prefix every turn. Automatic cache
		// placement lets each turn read the previous ones at the cached rate.
		CacheControl: sdk.NewBetaCacheControlEphemeralParam(),
	}
	if req.System != "" {
		params.System = []sdk.BetaTextBlockParam{{Text: req.System}}
	}
	if req.Model.Temperature != nil {
		params.Temperature = sdk.Float(*req.Model.Temperature)
	}
	if req.Model.Effort != "" {
		params.OutputConfig = sdk.BetaOutputConfigParam{Effort: sdk.BetaOutputConfigEffort(req.Model.Effort)}
	}
	// Server-side fallbacks re-run a declined request on another model inside
	// the same call. The two forms are gated by different beta headers.
	switch fb := req.Model.Fallbacks; {
	case fb.Default:
		params.Fallbacks = sdk.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
		params.Betas = append(params.Betas, sdk.AnthropicBetaServerSideFallback2026_07_01)
	case len(fb.Models) > 0:
		entries := make([]sdk.BetaFallbackParam, len(fb.Models))
		for i, model := range fb.Models {
			entries[i] = sdk.BetaFallbackParam{Model: sdk.Model(model)}
		}
		params.Fallbacks = sdk.BetaFallbacksParamUnion{OfBetaFallbackArray: entries}
		params.Betas = append(params.Betas, sdk.AnthropicBetaServerSideFallback2026_06_01)
	}

	msg, err := p.client.Beta.Messages.New(ctx, params)
	if err != nil {
		return nil, wrapError(err)
	}
	return fromMessage(msg), nil
}

func toMessages(in []llm.Message) ([]sdk.BetaMessageParam, error) {
	out := make([]sdk.BetaMessageParam, 0, len(in))
	for i, m := range in {
		// Replay our own assistant turns exactly as the API returned them.
		// Thinking blocks are signed and only valid when sent back unchanged.
		if m.Role == llm.RoleAssistant && m.Native != nil && m.Native.Provider == Name {
			var native sdk.BetaMessage
			if err := json.Unmarshal(m.Native.Data, &native); err != nil {
				return nil, fmt.Errorf("anthropic: message %d: decode native turn: %w", i, err)
			}
			out = append(out, native.ToParam())
			continue
		}

		blocks := make([]sdk.BetaContentBlockParamUnion, 0, len(m.Content))
		for _, b := range m.Content {
			switch b.Type {
			case llm.BlockText:
				blocks = append(blocks, sdk.NewBetaTextBlock(b.Text))
			case llm.BlockToolUse:
				blocks = append(blocks, sdk.NewBetaToolUseBlock(b.ToolUseID, b.Input, b.ToolName))
			case llm.BlockToolResult:
				blocks = append(blocks, sdk.NewBetaToolResultBlock(b.ToolUseID, b.Text, b.IsError))
			default:
				return nil, fmt.Errorf("anthropic: message %d: unsupported block type %q", i, b.Type)
			}
		}
		role := sdk.BetaMessageParamRoleUser
		if m.Role == llm.RoleAssistant {
			role = sdk.BetaMessageParamRoleAssistant
		}
		out = append(out, sdk.BetaMessageParam{Role: role, Content: blocks})
	}
	return out, nil
}

func toTools(defs []llm.ToolDef) ([]sdk.BetaToolUnionParam, error) {
	if len(defs) == 0 {
		return nil, nil
	}
	out := make([]sdk.BetaToolUnionParam, 0, len(defs))
	for _, def := range defs {
		schema, err := toInputSchema(def.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("anthropic: tool %q: %w", def.Name, err)
		}
		tool := sdk.BetaToolParam{Name: def.Name, InputSchema: schema}
		if def.Description != "" {
			tool.Description = sdk.String(def.Description)
		}
		out = append(out, sdk.BetaToolUnionParam{OfTool: &tool})
	}
	return out, nil
}

// toInputSchema splits a JSON Schema object into the fields the SDK models
// explicitly and passes everything else (additionalProperties, $defs, ...)
// through untouched.
func toInputSchema(raw json.RawMessage) (sdk.BetaToolInputSchemaParam, error) {
	var schema sdk.BetaToolInputSchemaParam
	if len(raw) == 0 {
		return schema, nil
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return schema, fmt.Errorf("input schema is not a JSON object: %w", err)
	}
	if props, ok := fields["properties"]; ok {
		schema.Properties = props
	}
	if required, ok := fields["required"].([]any); ok {
		for _, r := range required {
			if name, ok := r.(string); ok {
				schema.Required = append(schema.Required, name)
			}
		}
	}
	delete(fields, "type")
	delete(fields, "properties")
	delete(fields, "required")
	if len(fields) > 0 {
		schema.ExtraFields = fields
	}
	return schema, nil
}

func fromMessage(msg *sdk.BetaMessage) *llm.Response {
	out := llm.Message{
		Role:   llm.RoleAssistant,
		Native: &llm.Native{Provider: Name, Data: json.RawMessage(msg.RawJSON())},
	}
	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case sdk.BetaTextBlock:
			out.Content = append(out.Content, llm.Text(b.Text))
		case sdk.BetaToolUseBlock:
			out.Content = append(out.Content, llm.ToolUse(b.ID, b.Name, json.RawMessage(b.JSON.Input.Raw())))
		}
		// Thinking and other provider-specific blocks have no neutral form;
		// they travel in Native.
	}

	resp := &llm.Response{
		Message: out,
		Model:   string(msg.Model),
		Usage: llm.Usage{
			InputTokens:      msg.Usage.InputTokens,
			OutputTokens:     msg.Usage.OutputTokens,
			CacheReadTokens:  msg.Usage.CacheReadInputTokens,
			CacheWriteTokens: msg.Usage.CacheCreationInputTokens,
		},
	}
	switch msg.StopReason {
	case sdk.BetaStopReasonToolUse:
		resp.StopReason = llm.StopToolUse
	case sdk.BetaStopReasonMaxTokens:
		resp.StopReason = llm.StopMaxTokens
	case sdk.BetaStopReasonRefusal:
		// A refusal arrives as HTTP 200, possibly with no content at all.
		resp.StopReason = llm.StopRefusal
		resp.RefusalCategory = string(msg.StopDetails.Category)
	default:
		resp.StopReason = llm.StopEndTurn
	}
	return resp
}

func wrapError(err error) error {
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) {
		return &llm.Error{
			Provider:  Name,
			Status:    apiErr.StatusCode,
			Retryable: llm.RetryableStatus(apiErr.StatusCode),
			Err:       err,
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &llm.Error{Provider: Name, Err: err}
	}
	// No HTTP response: a transport failure, worth retrying.
	return &llm.Error{Provider: Name, Retryable: true, Err: err}
}
