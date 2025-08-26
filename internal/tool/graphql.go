package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/wojtekmanke/roster/internal/spec"
)

// maxResponseBytes bounds what a tool may put into the model's context.
const maxResponseBytes = 256 << 10

// graphqlHandler returns a handler that runs the declared operation with the
// tool input as its variables and returns the response's data as JSON.
func (b *Builder) graphqlHandler(decl *spec.GraphQL) (Handler, error) {
	endpoint, err := b.expand(decl.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("graphql endpoint: %w", err)
	}
	headers, err := b.expandHeaders(decl.Headers)
	if err != nil {
		return nil, fmt.Errorf("graphql: %w", err)
	}
	client := b.httpClient()
	query := decl.Query

	return func(ctx context.Context, input json.RawMessage) (string, error) {
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		body, err := json.Marshal(map[string]any{"query": query, "variables": input})
		if err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		for name, values := range headers {
			req.Header[name] = values
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("graphql request failed: %w", err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		if err != nil {
			return "", fmt.Errorf("graphql response: %w", err)
		}
		if len(raw) > maxResponseBytes {
			return "", fmt.Errorf("graphql response is larger than %d bytes; ask for fewer fields or rows", maxResponseBytes)
		}

		var result struct {
			Data   json.RawMessage `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return "", fmt.Errorf("graphql endpoint returned HTTP %d with a non-JSON body", resp.StatusCode)
		}
		if len(result.Errors) > 0 {
			messages := make([]string, len(result.Errors))
			for i, e := range result.Errors {
				messages[i] = e.Message
			}
			return "", fmt.Errorf("graphql error: %s", strings.Join(messages, "; "))
		}
		if resp.StatusCode >= 400 {
			return "", fmt.Errorf("graphql endpoint returned HTTP %d", resp.StatusCode)
		}
		if len(result.Data) == 0 {
			return "null", nil
		}
		return string(result.Data), nil
	}, nil
}
