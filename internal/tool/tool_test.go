package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wojtekmanke/roster/internal/spec"
	"github.com/wojtekmanke/roster/internal/tool"
)

func echo(_ context.Context, input json.RawMessage) (string, error) { return string(input), nil }

func TestSetValidatesInput(t *testing.T) {
	set, err := tool.NewSet(tool.Tool{
		Name:        "get_order",
		Description: "Look up an order",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"limit":{"type":"integer","minimum":1}},"required":["id"],"additionalProperties":false}`),
		Handler:     echo,
	}, tool.Tool{Name: "ping", Handler: echo})
	if err != nil {
		t.Fatal(err)
	}

	order, _ := set.Get("get_order")
	tests := []struct {
		input string
		valid bool
	}{
		{`{"id":"O-1"}`, true},
		{`{"id":"O-1","limit":5}`, true},
		{`{}`, false},
		{`{"id":7}`, false},
		{`{"id":"O-1","limit":0}`, false},
		{`{"id":"O-1","extra":true}`, false},
		{`"{not json"`, false},
		{`{bad`, false},
	}
	for _, tt := range tests {
		if err := order.Validate(json.RawMessage(tt.input)); (err == nil) != tt.valid {
			t.Errorf("Validate(%s) = %v, want valid=%v", tt.input, err, tt.valid)
		}
	}

	// A tool with no declared schema accepts an empty object, or nothing at all.
	ping, _ := set.Get("ping")
	if err := ping.Validate(nil); err != nil {
		t.Errorf("empty input rejected: %v", err)
	}

	defs := set.Defs()
	if len(defs) != 2 || defs[0].Name != "get_order" || defs[1].Name != "ping" {
		t.Errorf("Defs() = %+v, want declaration order", defs)
	}
	if string(defs[1].InputSchema) == "" {
		t.Error("schemaless tool was offered to the model without an input schema")
	}
}

func TestNewSetRejectsBadTools(t *testing.T) {
	if _, err := tool.NewSet(tool.Tool{Name: "a", Handler: echo}, tool.Tool{Name: "a", Handler: echo}); err == nil {
		t.Error("duplicate names accepted")
	}
	if _, err := tool.NewSet(tool.Tool{Name: "a"}); err == nil {
		t.Error("tool without a handler accepted")
	}
	if _, err := tool.NewSet(tool.Tool{Name: "a", Handler: echo, InputSchema: json.RawMessage(`{"type":12}`)}); err == nil {
		t.Error("invalid schema accepted")
	}
}

func TestBuildBuiltin(t *testing.T) {
	b := &tool.Builder{Builtins: tool.Builtins{
		"refund": {
			Description:      "Refund an order",
			InputSchema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
			RequiresApproval: true,
			SideEffect:       true,
			Handler:          echo,
		},
	}}
	s := &spec.Spec{Tools: []spec.Tool{{Name: "refund_order", Builtin: "refund"}}}
	set, err := b.Build(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()

	got, _ := set.Get("refund_order")
	if got.Description != "Refund an order" || !got.RequiresApproval || !got.SideEffect {
		t.Errorf("builtin metadata not inherited: %+v", got)
	}
	if err := got.Validate(json.RawMessage(`{}`)); err == nil {
		t.Error("builtin schema not applied")
	}

	s.Tools[0].Builtin = "missing"
	if _, err := b.Build(context.Background(), s); err == nil || !strings.Contains(err.Error(), "have: refund") {
		t.Errorf("err = %v, want it to list the builtins", err)
	}
}

func TestBuildGraphQL(t *testing.T) {
	var gotBody map[string]any
	var gotAuth string
	response := `{"data":{"order":{"id":"O-1","status":"shipped"}}}`
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &gotBody)
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, response)
	}))
	defer srv.Close()

	env := map[string]string{"SHOP_URL": srv.URL, "SHOP_TOKEN": "s3cret"}
	b := &tool.Builder{Getenv: func(k string) string { return env[k] }}
	s := &spec.Spec{Tools: []spec.Tool{{
		Name:        "get_order",
		Description: "Look up an order",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "required": []any{"id"}},
		GraphQL: &spec.GraphQL{
			Endpoint: "${SHOP_URL}/graphql",
			Query:    "query($id: ID!) { order(id: $id) { id status } }",
			Headers:  map[string]string{"Authorization": "Bearer ${SHOP_TOKEN}"},
		},
	}}}
	set, err := b.Build(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	get, _ := set.Get("get_order")

	out, err := get.Handler(context.Background(), json.RawMessage(`{"id":"O-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"order":{"id":"O-1","status":"shipped"}}` {
		t.Errorf("result = %s, want the response's data", out)
	}
	if gotAuth != "Bearer s3cret" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotBody["query"] != "query($id: ID!) { order(id: $id) { id status } }" {
		t.Errorf("query = %v", gotBody["query"])
	}
	if vars := gotBody["variables"].(map[string]any); vars["id"] != "O-1" {
		t.Errorf("variables = %v", vars)
	}

	response = `{"data":null,"errors":[{"message":"order not found"},{"message":"try again"}]}`
	if _, err := get.Handler(context.Background(), json.RawMessage(`{"id":"O-2"}`)); err == nil || !strings.Contains(err.Error(), "order not found; try again") {
		t.Errorf("err = %v, want the GraphQL error messages", err)
	}

	response, status = `<html>bad gateway</html>`, 502
	if _, err := get.Handler(context.Background(), json.RawMessage(`{"id":"O-2"}`)); err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("err = %v, want the HTTP status", err)
	}

	response, status = `{"data":"`+strings.Repeat("x", 300<<10)+`"}`, 200
	if _, err := get.Handler(context.Background(), json.RawMessage(`{"id":"O-2"}`)); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("err = %v, want a size error", err)
	}
}

func TestBuildFailsOnUnsetEnvironment(t *testing.T) {
	b := &tool.Builder{Getenv: func(string) string { return "" }}
	s := &spec.Spec{Tools: []spec.Tool{{
		Name:    "get_order",
		GraphQL: &spec.GraphQL{Endpoint: "${SHOP_URL}", Query: "{ ok }"},
	}}}
	_, err := b.Build(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), "SHOP_URL is not set") {
		t.Fatalf("err = %v, want it to name the unset variable", err)
	}
}

// mcpServer serves two tools over streamable HTTP and records the
// Authorization header it sees.
func mcpServer(t *testing.T, auth *string) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)

	type lookupInput struct {
		SKU string `json:"sku" jsonschema:"the SKU to look up"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "stock_level", Description: "Units on hand for a SKU"},
		func(_ context.Context, _ *mcp.CallToolRequest, in lookupInput) (*mcp.CallToolResult, any, error) {
			if in.SKU == "missing" {
				return nil, nil, errors.New("unknown SKU")
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"sku":"` + in.SKU + `","on_hand":12}`}}}, nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "delete_everything", Description: "Do not offer this"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		})

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			*auth = a
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBuildMCPServer(t *testing.T) {
	var auth string
	srv := mcpServer(t, &auth)

	env := map[string]string{"WMS_TOKEN": "tok"}
	b := &tool.Builder{Getenv: func(k string) string { return env[k] }}
	s := &spec.Spec{MCPServers: []spec.MCPServer{{
		Name:    "warehouse",
		URL:     srv.URL,
		Headers: map[string]string{"Authorization": "Bearer ${WMS_TOKEN}"},
		Tools:   []spec.MCPTool{{Name: "stock_level"}},
	}}}
	set, err := b.Build(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()

	if names := set.Names(); len(names) != 1 || names[0] != "stock_level" {
		t.Fatalf("tools = %v, want only the allowed one", names)
	}
	stock, _ := set.Get("stock_level")
	if stock.Description != "Units on hand for a SKU" {
		t.Errorf("description = %q", stock.Description)
	}
	if err := stock.Validate(json.RawMessage(`{}`)); err == nil {
		t.Error("the server's input schema was not applied")
	}

	out, err := stock.Handler(context.Background(), json.RawMessage(`{"sku":"TEE-1"}`))
	if err != nil || out != `{"sku":"TEE-1","on_hand":12}` {
		t.Fatalf("call = %q, %v", out, err)
	}
	if auth != "Bearer tok" {
		t.Errorf("Authorization = %q", auth)
	}

	if _, err := stock.Handler(context.Background(), json.RawMessage(`{"sku":"missing"}`)); err == nil || !strings.Contains(err.Error(), "unknown SKU") {
		t.Errorf("err = %v, want the tool's error", err)
	}
}

func TestBuildMCPServerRejectsUnknownTool(t *testing.T) {
	var auth string
	srv := mcpServer(t, &auth)
	s := &spec.Spec{MCPServers: []spec.MCPServer{{
		Name:  "warehouse",
		URL:   srv.URL,
		Tools: []spec.MCPTool{{Name: "restock"}},
	}}}
	_, err := (&tool.Builder{}).Build(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), `does not offer a tool named "restock"`) {
		t.Fatalf("err = %v", err)
	}
}
