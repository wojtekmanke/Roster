// Package tool turns the tools declared in an agent spec into callable
// functions with validated inputs.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/spec"
)

// Handler executes a tool call and returns the text shown to the model.
// A returned error is also shown to the model, marked as an error, so it can
// correct itself; it does not fail the run.
type Handler func(ctx context.Context, input json.RawMessage) (string, error)

// Tool is one callable tool.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	// SideEffect marks tools that change the outside world.
	SideEffect bool
	// RequiresApproval means a person must approve each call.
	RequiresApproval bool
	Handler          Handler

	schema *jsonschema.Resolved
}

// emptyObjectSchema is used for tools that declare no input.
var emptyObjectSchema = json.RawMessage(`{"type":"object","properties":{}}`)

// compile resolves the tool's input schema so calls can be validated.
func (t *Tool) compile() error {
	if len(t.InputSchema) == 0 {
		t.InputSchema = emptyObjectSchema
	}
	resolved, err := CompileSchema(t.InputSchema)
	if err != nil {
		return fmt.Errorf("tool %q: input schema: %w", t.Name, err)
	}
	t.schema = resolved
	return nil
}

// Validate checks a call's input against the tool's schema.
func (t *Tool) Validate(input json.RawMessage) error {
	return ValidateJSON(t.schema, input)
}

// CompileSchema parses and resolves a JSON Schema document.
func CompileSchema(raw json.RawMessage) (*jsonschema.Resolved, error) {
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	return schema.Resolve(nil)
}

// ValidateJSON checks a JSON document against a resolved schema.
func ValidateJSON(schema *jsonschema.Resolved, doc json.RawMessage) error {
	if len(doc) == 0 {
		doc = json.RawMessage(`{}`)
	}
	var value any
	if err := json.Unmarshal(doc, &value); err != nil {
		return fmt.Errorf("not valid JSON: %w", err)
	}
	return schema.Validate(value)
}

// Set is the resolved toolset of one agent.
type Set struct {
	tools   map[string]*Tool
	order   []string
	closers []func() error
}

// NewSet builds a Set from tools, compiling their schemas.
func NewSet(tools ...Tool) (*Set, error) {
	s := &Set{tools: map[string]*Tool{}}
	for _, t := range tools {
		if err := s.add(t); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Set) add(t Tool) error {
	if _, dup := s.tools[t.Name]; dup {
		return fmt.Errorf("tool %q is defined twice", t.Name)
	}
	if t.Handler == nil {
		return fmt.Errorf("tool %q has no handler", t.Name)
	}
	if err := t.compile(); err != nil {
		return err
	}
	s.tools[t.Name] = &t
	s.order = append(s.order, t.Name)
	return nil
}

// Get returns the named tool.
func (s *Set) Get(name string) (*Tool, bool) {
	t, ok := s.tools[name]
	return t, ok
}

// Names returns the tool names in declaration order.
func (s *Set) Names() []string { return append([]string(nil), s.order...) }

// Defs returns the tool definitions to offer the model, in declaration order.
func (s *Set) Defs() []llm.ToolDef {
	defs := make([]llm.ToolDef, 0, len(s.order))
	for _, name := range s.order {
		t := s.tools[name]
		defs = append(defs, llm.ToolDef{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return defs
}

// Close releases connections held by the set, such as MCP sessions.
func (s *Set) Close() error {
	var errs []error
	for _, closer := range s.closers {
		if err := closer(); err != nil {
			errs = append(errs, err)
		}
	}
	s.closers = nil
	return errors.Join(errs...)
}

// Builtins are tools implemented in Go that specs refer to by name.
type Builtins map[string]Tool

// Builder resolves the tools declared in a spec.
type Builder struct {
	Builtins Builtins
	// HTTPClient is used for GraphQL and MCP calls. Nil means a client with a
	// 30-second timeout.
	HTTPClient *http.Client
	// Getenv resolves ${NAME} references. Nil means os.Getenv.
	Getenv func(string) string
}

func (b *Builder) httpClient() *http.Client {
	if b.HTTPClient != nil {
		return b.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Build resolves every tool the spec declares. The caller must Close the
// returned set.
func (b *Builder) Build(ctx context.Context, s *spec.Spec) (*Set, error) {
	set := &Set{tools: map[string]*Tool{}}
	fail := func(err error) (*Set, error) {
		set.Close()
		return nil, err
	}

	for _, decl := range s.Tools {
		t := Tool{
			Name:             decl.Name,
			Description:      decl.Description,
			SideEffect:       decl.SideEffect,
			RequiresApproval: decl.RequiresApproval,
		}
		if decl.InputSchema != nil {
			raw, err := json.Marshal(decl.InputSchema)
			if err != nil {
				return fail(fmt.Errorf("tool %q: %w", decl.Name, err))
			}
			t.InputSchema = raw
		}

		switch {
		case decl.GraphQL != nil:
			handler, err := b.graphqlHandler(decl.GraphQL)
			if err != nil {
				return fail(fmt.Errorf("tool %q: %w", decl.Name, err))
			}
			t.Handler = handler
		case decl.Builtin != "":
			builtin, ok := b.Builtins[decl.Builtin]
			if !ok {
				return fail(fmt.Errorf("tool %q: no builtin named %q (have: %s)", decl.Name, decl.Builtin, strings.Join(b.builtinNames(), ", ")))
			}
			t.Handler = builtin.Handler
			// The spec may tighten a builtin's flags but never loosen them.
			t.SideEffect = t.SideEffect || builtin.SideEffect
			t.RequiresApproval = t.RequiresApproval || builtin.RequiresApproval
			if t.Description == "" {
				t.Description = builtin.Description
			}
			if t.InputSchema == nil {
				t.InputSchema = builtin.InputSchema
			}
		}
		if err := set.add(t); err != nil {
			return fail(err)
		}
	}

	for _, server := range s.MCPServers {
		if err := b.addMCPServer(ctx, set, server); err != nil {
			return fail(fmt.Errorf("mcp server %q: %w", server.Name, err))
		}
	}
	return set, nil
}

func (b *Builder) builtinNames() []string {
	names := make([]string, 0, len(b.Builtins))
	for name := range b.Builtins {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expand replaces ${NAME} references with environment values. A reference to
// an unset variable is an error rather than an empty string, so a missing
// secret cannot turn into an unauthenticated request.
func (b *Builder) expand(s string) (string, error) {
	getenv := b.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	var missing []string
	out := envRef.ReplaceAllStringFunc(s, func(ref string) string {
		name := envRef.FindStringSubmatch(ref)[1]
		value := getenv(name)
		if value == "" {
			missing = append(missing, name)
		}
		return value
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("environment variable %s is not set", strings.Join(missing, ", "))
	}
	return out, nil
}

func (b *Builder) expandHeaders(in map[string]string) (http.Header, error) {
	headers := http.Header{}
	for name, value := range in {
		expanded, err := b.expand(value)
		if err != nil {
			return nil, fmt.Errorf("header %s: %w", name, err)
		}
		headers.Set(name, expanded)
	}
	return headers, nil
}
