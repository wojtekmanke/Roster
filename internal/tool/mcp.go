package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wojtekmanke/roster/internal/spec"
)

// clientInfo identifies Roster to MCP servers.
var clientInfo = &mcp.Implementation{Name: "roster", Version: "1"}

// addMCPServer connects to a server and adds the tools the spec allows.
func (b *Builder) addMCPServer(ctx context.Context, set *Set, server spec.MCPServer) error {
	transport, err := b.mcpTransport(server)
	if err != nil {
		return err
	}
	session, err := mcp.NewClient(clientInfo, nil).Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	set.closers = append(set.closers, session.Close)
	return addMCPTools(ctx, set, session, server.Tools)
}

func (b *Builder) mcpTransport(server spec.MCPServer) (mcp.Transport, error) {
	if len(server.Command) > 0 {
		return &mcp.CommandTransport{Command: exec.Command(server.Command[0], server.Command[1:]...)}, nil
	}
	endpoint, err := b.expand(server.URL)
	if err != nil {
		return nil, fmt.Errorf("url: %w", err)
	}
	headers, err := b.expandHeaders(server.Headers)
	if err != nil {
		return nil, err
	}
	client := b.httpClient()
	if len(headers) > 0 {
		withHeaders := *client
		withHeaders.Transport = &headerTransport{base: client.Transport, headers: headers}
		client = &withHeaders
	}
	return &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: client}, nil
}

// headerTransport adds fixed headers, such as credentials, to every request.
type headerTransport struct {
	base    http.RoundTripper
	headers http.Header
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for name, values := range t.headers {
		req.Header[name] = values
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// addMCPTools lists the server's tools and registers the allowed ones. A tool
// the spec names but the server does not offer is an error: the agent's
// instructions were written assuming it exists.
func addMCPTools(ctx context.Context, set *Set, session *mcp.ClientSession, allowed []spec.MCPTool) error {
	offered := map[string]*mcp.Tool{}
	var cursor string
	for {
		page, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return fmt.Errorf("list tools: %w", err)
		}
		for _, t := range page.Tools {
			offered[t.Name] = t
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}

	for _, allow := range allowed {
		remote, ok := offered[allow.Name]
		if !ok {
			return fmt.Errorf("server does not offer a tool named %q", allow.Name)
		}
		schema, err := json.Marshal(remote.InputSchema)
		if err != nil {
			return fmt.Errorf("tool %q: input schema: %w", allow.Name, err)
		}
		name := allow.Name
		err = set.add(Tool{
			Name:             name,
			Description:      remote.Description,
			InputSchema:      schema,
			SideEffect:       allow.SideEffect,
			RequiresApproval: allow.RequiresApproval,
			Handler: func(ctx context.Context, input json.RawMessage) (string, error) {
				return callMCPTool(ctx, session, name, input)
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func callMCPTool(ctx context.Context, session *mcp.ClientSession, name string, input json.RawMessage) (string, error) {
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: input})
	if err != nil {
		return "", fmt.Errorf("mcp call failed: %w", err)
	}

	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	text := strings.Join(parts, "\n")
	if text == "" && result.StructuredContent != nil {
		structured, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return "", err
		}
		text = string(structured)
	}
	if result.IsError {
		if text == "" {
			text = "tool reported an error"
		}
		return "", errors.New(text)
	}
	return text, nil
}
