// Package spec defines the YAML document that describes an agent: what it is
// told, which model and tools it uses, what starts it, and how it is judged.
package spec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/schedule"
)

// SubmitTool is the reserved tool an agent calls to hand in its structured result.
const SubmitTool = "submit_result"

// Spec is one agent definition.
type Spec struct {
	Name        string          `yaml:"name"`
	Description string          `yaml:"description,omitempty"`
	Model       llm.ModelConfig `yaml:"model"`
	// Instructions is the system prompt.
	Instructions string `yaml:"instructions"`
	// Prompt is a Go text/template rendered with the run's input to produce
	// the first user message. Empty means the input is shown as JSON.
	Prompt     string      `yaml:"prompt,omitempty"`
	Triggers   []Trigger   `yaml:"triggers,omitempty"`
	Tools      []Tool      `yaml:"tools,omitempty"`
	MCPServers []MCPServer `yaml:"mcp_servers,omitempty"`
	Output     *Output     `yaml:"output,omitempty"`
	Limits     Limits      `yaml:"limits,omitempty"`
	KPIs       []KPI       `yaml:"kpis,omitempty"`
	Review     Review      `yaml:"review,omitempty"`
	Shadow     []Shadow    `yaml:"shadow,omitempty"`
}

// Trigger starts runs. Exactly one of Event, Schedule or Webhook is set.
type Trigger struct {
	// Event is the name of an event to react to, such as "ticket.created".
	Event string `yaml:"event,omitempty"`
	// Schedule is "every <duration>" or a five-field cron expression.
	Schedule string `yaml:"schedule,omitempty"`
	// Webhook exposes POST /hooks/<agent name>.
	Webhook bool `yaml:"webhook,omitempty"`
	// Input is the fixed input of scheduled runs.
	Input map[string]any `yaml:"input,omitempty"`
}

// Tool declares something the agent can call. Exactly one of GraphQL or
// Builtin says how the call is carried out.
type Tool struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description,omitempty"`
	InputSchema map[string]any `yaml:"input_schema,omitempty"`
	// GraphQL runs an operation against an HTTP endpoint, with the tool input
	// as the operation's variables.
	GraphQL *GraphQL `yaml:"graphql,omitempty"`
	// Builtin names a tool registered in Go.
	Builtin string `yaml:"builtin,omitempty"`
	// SideEffect marks tools that change the outside world. Shadow runs
	// simulate them instead of executing them.
	SideEffect bool `yaml:"side_effect,omitempty"`
	// RequiresApproval suspends the run until a person approves the call.
	RequiresApproval bool `yaml:"requires_approval,omitempty"`
}

// GraphQL is a declared GraphQL operation. Endpoint and header values may
// reference environment variables as ${NAME}.
type GraphQL struct {
	Endpoint string            `yaml:"endpoint"`
	Query    string            `yaml:"query"`
	Headers  map[string]string `yaml:"headers,omitempty"`
}

// MCPServer connects the agent to a Model Context Protocol server. Exactly
// one of URL (streamable HTTP) or Command (stdio) is set.
type MCPServer struct {
	Name    string            `yaml:"name"`
	URL     string            `yaml:"url,omitempty"`
	Command []string          `yaml:"command,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
	// Tools lists the server's tools the agent may use. Tools not listed are
	// not offered to the model.
	Tools []MCPTool `yaml:"tools"`
}

// MCPTool allows one tool from an MCP server.
type MCPTool struct {
	Name             string `yaml:"name"`
	SideEffect       bool   `yaml:"side_effect,omitempty"`
	RequiresApproval bool   `yaml:"requires_approval,omitempty"`
}

// Output declares the structured result the agent must submit.
type Output struct {
	Schema map[string]any `yaml:"schema"`
}

// Limits bound a single run.
type Limits struct {
	// MaxSteps caps the number of model calls.
	MaxSteps int `yaml:"max_steps,omitempty"`
	// MaxCostUSD stops the run once its model spend passes this amount.
	MaxCostUSD float64 `yaml:"max_cost_usd,omitempty"`
	// Timeout bounds the run's active time, excluding time spent waiting for approval.
	Timeout Duration `yaml:"timeout,omitempty"`
	// ToolTimeout bounds each tool call.
	ToolTimeout Duration `yaml:"tool_timeout,omitempty"`
}

// KPI is one number the agent is judged on.
type KPI struct {
	Name string `yaml:"name"`
	// Grader names the grader that produces the metric.
	Grader string `yaml:"grader"`
	// Metric selects one of the grader's metrics. Empty means its primary one.
	Metric string `yaml:"metric,omitempty"`
	// Aggregate combines per-run values over the review window:
	// mean (default), sum, min, max, p50 or p95.
	Aggregate string         `yaml:"aggregate,omitempty"`
	Target    *Target        `yaml:"target,omitempty"`
	Params    map[string]any `yaml:"params,omitempty"`
}

// Review is the policy applied to the agent's scorecard.
type Review struct {
	// Window is how many of the most recent graded runs count.
	Window int `yaml:"window,omitempty"`
	// MinRuns is how many samples a KPI needs before it can breach.
	MinRuns int `yaml:"min_runs,omitempty"`
	// OnBreach is "pause" (default) or "alert".
	OnBreach string `yaml:"on_breach,omitempty"`
}

// Shadow re-runs a sample of the agent's inputs on another model, with side
// effects simulated, so the two can be compared on the same work.
type Shadow struct {
	Model llm.ModelConfig `yaml:",inline"`
	// SampleRate is the fraction of runs to shadow, in (0, 1]. Default 1.
	SampleRate float64 `yaml:"sample_rate,omitempty"`
}

// Review actions.
const (
	OnBreachPause = "pause"
	OnBreachAlert = "alert"
)

// Defaults applied by Parse.
const (
	DefaultMaxSteps    = 12
	DefaultTimeout     = 5 * time.Minute
	DefaultToolTimeout = 30 * time.Second
	DefaultWindow      = 50
	DefaultMinRuns     = 20
)

// Duration is a time.Duration that reads from YAML strings such as "90s".
type Duration time.Duration

// UnmarshalYAML parses a duration string.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalYAML writes the duration as a string.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Target is a comparison a KPI's aggregate must satisfy, written ">= 0.85".
type Target struct {
	Op    string
	Value float64
}

var targetPattern = regexp.MustCompile(`^(>=|<=|==|>|<)\s*(-?[0-9]+(?:\.[0-9]+)?)$`)

// ParseTarget parses expressions such as ">= 0.85" or "< 3".
func ParseTarget(s string) (Target, error) {
	m := targetPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Target{}, fmt.Errorf("target %q must look like \">= 0.85\"", s)
	}
	value, err := strconv.ParseFloat(m[2], 64)
	if err != nil {
		return Target{}, err
	}
	return Target{Op: m[1], Value: value}, nil
}

// UnmarshalYAML parses a target expression.
func (t *Target) UnmarshalYAML(node *yaml.Node) error {
	parsed, err := ParseTarget(node.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	*t = parsed
	return nil
}

// MarshalYAML writes the target back as an expression.
func (t Target) MarshalYAML() (any, error) { return t.String(), nil }

// String formats the target as it is written in a spec.
func (t Target) String() string {
	return t.Op + " " + strconv.FormatFloat(t.Value, 'f', -1, 64)
}

// Met reports whether v satisfies the target.
func (t Target) Met(v float64) bool {
	switch t.Op {
	case ">=":
		return v >= t.Value
	case "<=":
		return v <= t.Value
	case ">":
		return v > t.Value
	case "<":
		return v < t.Value
	case "==":
		return v == t.Value
	}
	return false
}

// Parse decodes, defaults and validates an agent spec. Unknown fields are
// errors, so a typo cannot silently disable a limit or a KPI.
func Parse(data []byte) (*Spec, error) {
	var s Spec
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("spec: %w", err)
	}
	s.applyDefaults()
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// Hash identifies a spec document's exact bytes.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *Spec) applyDefaults() {
	if s.Limits.MaxSteps == 0 {
		s.Limits.MaxSteps = DefaultMaxSteps
	}
	if s.Limits.Timeout == 0 {
		s.Limits.Timeout = Duration(DefaultTimeout)
	}
	if s.Limits.ToolTimeout == 0 {
		s.Limits.ToolTimeout = Duration(DefaultToolTimeout)
	}
	if s.Review.Window == 0 {
		s.Review.Window = DefaultWindow
	}
	if s.Review.MinRuns == 0 {
		s.Review.MinRuns = min(DefaultMinRuns, s.Review.Window)
	}
	if s.Review.OnBreach == "" {
		s.Review.OnBreach = OnBreachPause
	}
	for i := range s.Tools {
		// Anything that needs a person's sign-off changes the world.
		if s.Tools[i].RequiresApproval {
			s.Tools[i].SideEffect = true
		}
	}
	for i := range s.MCPServers {
		for j := range s.MCPServers[i].Tools {
			if s.MCPServers[i].Tools[j].RequiresApproval {
				s.MCPServers[i].Tools[j].SideEffect = true
			}
		}
	}
	for i := range s.KPIs {
		if s.KPIs[i].Aggregate == "" {
			s.KPIs[i].Aggregate = "mean"
		}
	}
	for i := range s.Shadow {
		if s.Shadow[i].SampleRate == 0 {
			s.Shadow[i].SampleRate = 1
		}
	}
}

var (
	agentName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)
	toolName  = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
)

// Aggregates lists the valid values of KPI.Aggregate.
var Aggregates = []string{"mean", "sum", "min", "max", "p50", "p95"}

// Validate reports every problem in the spec, not just the first.
func (s *Spec) Validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if !agentName.MatchString(s.Name) {
		bad("name %q must be lowercase letters, digits and dashes, starting with a letter", s.Name)
	}
	if s.Model.Provider == "" || s.Model.Name == "" {
		bad("model.provider and model.name are required")
	}
	if strings.TrimSpace(s.Instructions) == "" {
		bad("instructions are required")
	}
	if s.Prompt != "" {
		if _, err := s.promptTemplate(); err != nil {
			bad("prompt: %v", err)
		}
	}

	for i, tr := range s.Triggers {
		kinds := 0
		if tr.Event != "" {
			kinds++
		}
		if tr.Schedule != "" {
			kinds++
			if _, err := schedule.Parse(tr.Schedule); err != nil {
				bad("triggers[%d]: %v", i, err)
			}
		}
		if tr.Webhook {
			kinds++
		}
		if kinds != 1 {
			bad("triggers[%d] must set exactly one of event, schedule or webhook", i)
		}
		if tr.Input != nil && tr.Schedule == "" {
			bad("triggers[%d]: input is only valid on a schedule trigger", i)
		}
	}

	seenTools := map[string]bool{}
	checkToolName := func(where, name string) {
		switch {
		case !toolName.MatchString(name):
			bad("%s: tool name %q must be 1-64 letters, digits, underscores or dashes", where, name)
		case name == SubmitTool:
			bad("%s: %q is reserved", where, SubmitTool)
		case seenTools[name]:
			bad("%s: duplicate tool name %q", where, name)
		}
		seenTools[name] = true
	}
	for i, tool := range s.Tools {
		where := fmt.Sprintf("tools[%d]", i)
		checkToolName(where, tool.Name)
		switch {
		case tool.GraphQL != nil && tool.Builtin != "":
			bad("%s (%s) must set only one of graphql or builtin", where, tool.Name)
		case tool.GraphQL != nil:
			if tool.GraphQL.Endpoint == "" || strings.TrimSpace(tool.GraphQL.Query) == "" {
				bad("%s (%s): graphql.endpoint and graphql.query are required", where, tool.Name)
			}
			if tool.Description == "" {
				bad("%s (%s): description is required so the model knows when to call it", where, tool.Name)
			}
		case tool.Builtin == "":
			bad("%s (%s) must set one of graphql or builtin", where, tool.Name)
		}
		if tool.InputSchema != nil {
			if err := checkObjectSchema(tool.InputSchema); err != nil {
				bad("%s (%s): input_schema: %v", where, tool.Name, err)
			}
		}
	}

	seenServers := map[string]bool{}
	for i, server := range s.MCPServers {
		where := fmt.Sprintf("mcp_servers[%d]", i)
		if server.Name == "" || seenServers[server.Name] {
			bad("%s: name is required and must be unique", where)
		}
		seenServers[server.Name] = true
		if (server.URL == "") == (len(server.Command) == 0) {
			bad("%s (%s) must set exactly one of url or command", where, server.Name)
		}
		if len(server.Tools) == 0 {
			bad("%s (%s): list the tools the agent may use", where, server.Name)
		}
		for _, tool := range server.Tools {
			checkToolName(where, tool.Name)
		}
	}

	if s.Output != nil {
		if err := checkObjectSchema(s.Output.Schema); err != nil {
			bad("output.schema: %v", err)
		}
	}

	if s.Limits.MaxSteps < 1 {
		bad("limits.max_steps must be at least 1")
	}
	if s.Limits.MaxCostUSD < 0 {
		bad("limits.max_cost_usd must not be negative")
	}
	if s.Limits.Timeout <= 0 || s.Limits.ToolTimeout <= 0 {
		bad("limits.timeout and limits.tool_timeout must be positive")
	}

	seenKPIs := map[string]bool{}
	for i, kpi := range s.KPIs {
		where := fmt.Sprintf("kpis[%d]", i)
		if kpi.Name == "" || seenKPIs[kpi.Name] {
			bad("%s: name is required and must be unique", where)
		}
		seenKPIs[kpi.Name] = true
		if kpi.Grader == "" {
			bad("%s (%s): grader is required", where, kpi.Name)
		}
		if !contains(Aggregates, kpi.Aggregate) {
			bad("%s (%s): aggregate %q must be one of %s", where, kpi.Name, kpi.Aggregate, strings.Join(Aggregates, ", "))
		}
	}

	if s.Review.Window < 1 {
		bad("review.window must be at least 1")
	}
	if s.Review.MinRuns < 1 || s.Review.MinRuns > s.Review.Window {
		bad("review.min_runs must be between 1 and review.window (%d)", s.Review.Window)
	}
	if s.Review.OnBreach != OnBreachPause && s.Review.OnBreach != OnBreachAlert {
		bad("review.on_breach must be %q or %q", OnBreachPause, OnBreachAlert)
	}

	for i, sh := range s.Shadow {
		if sh.Model.Provider == "" || sh.Model.Name == "" {
			bad("shadow[%d]: provider and name are required", i)
		}
		if sh.SampleRate <= 0 || sh.SampleRate > 1 {
			bad("shadow[%d]: sample_rate must be in (0, 1]", i)
		}
		if sh.Model.Provider == s.Model.Provider && sh.Model.Name == s.Model.Name {
			bad("shadow[%d]: shadowing the primary model compares it with itself", i)
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("spec %q is invalid:\n%w", s.Name, errors.Join(errs...))
}

func checkObjectSchema(schema map[string]any) error {
	if schema == nil {
		return errors.New("is required")
	}
	if schema["type"] != "object" {
		return errors.New(`must have type "object"`)
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func (s *Spec) promptTemplate() (*template.Template, error) {
	return template.New("prompt").Option("missingkey=error").Parse(s.Prompt)
}

// RenderPrompt builds the first user message from the run's input.
func (s *Spec) RenderPrompt(input json.RawMessage) (string, error) {
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	if s.Prompt == "" {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, input, "", "  "); err != nil {
			return "", fmt.Errorf("spec: input is not valid JSON: %w", err)
		}
		return "Input:\n" + pretty.String(), nil
	}
	tmpl, err := s.promptTemplate()
	if err != nil {
		return "", err
	}
	var data any
	if err := json.Unmarshal(input, &data); err != nil {
		return "", fmt.Errorf("spec: input is not valid JSON: %w", err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return "", fmt.Errorf("spec: render prompt: %w", err)
	}
	return out.String(), nil
}

// HasEvent reports whether the agent reacts to the named event.
func (s *Spec) HasEvent(name string) bool {
	for _, tr := range s.Triggers {
		if tr.Event == name {
			return true
		}
	}
	return false
}

// HasWebhook reports whether the agent accepts webhook calls.
func (s *Spec) HasWebhook() bool {
	for _, tr := range s.Triggers {
		if tr.Webhook {
			return true
		}
	}
	return false
}
