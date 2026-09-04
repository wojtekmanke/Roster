package server_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wojtekmanke/roster/internal/agent"
	"github.com/wojtekmanke/roster/internal/fleet"
	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/llm/llmtest"
	"github.com/wojtekmanke/roster/internal/pricing"
	"github.com/wojtekmanke/roster/internal/scoring"
	"github.com/wojtekmanke/roster/internal/server"
	"github.com/wojtekmanke/roster/internal/spec"
	"github.com/wojtekmanke/roster/internal/store/storetest"
	"github.com/wojtekmanke/roster/internal/tool"
)

const triageSpec = `
name: triage
description: Decides what to do with tickets.
model: {provider: scripted, name: primary-model}
instructions: Decide what to do with the ticket.
triggers:
  - event: ticket.created
  - webhook: true
  - schedule: every 1h
tools:
  - {name: refund, builtin: refund}
output:
  schema: {type: object, properties: {action: {type: string}}, required: [action]}
kpis:
  - {name: accuracy, grader: expected, target: ">= 0.75"}
  - {name: cost, grader: cost}
review: {window: 10, min_runs: 2}
`

type api struct {
	t     *testing.T
	srv   *httptest.Server
	svc   *fleet.Service
	model *llmtest.Scripted
	token string
}

func newAPI(t *testing.T, token string) *api {
	t.Helper()
	db := storetest.New(t)
	model := llmtest.NewScripted()
	model.Func = func(req *llm.Request) (*llm.Response, error) {
		return llmtest.Call(spec.SubmitTool, map[string]string{"action": "reply"}), nil
	}
	svc := &fleet.Service{
		Store: db,
		Runner: &agent.Runner{
			Providers: llm.Registry{"scripted": model},
			Pricing:   pricing.Table{"scripted": {"*": {Input: 100, Output: 200}}},
			Recorder:  db,
		},
		Tools: &tool.Builder{Builtins: tool.Builtins{
			"refund": {
				Description:      "Refund an order",
				RequiresApproval: true,
				Handler:          func(context.Context, json.RawMessage) (string, error) { return "refunded", nil },
			},
		}},
		Graders: scoring.Builtin(),
	}

	web := t.TempDir()
	os.WriteFile(filepath.Join(web, "index.html"), []byte("<html>dashboard</html>"), 0o600)
	os.MkdirAll(filepath.Join(web, "assets"), 0o700)
	os.WriteFile(filepath.Join(web, "assets", "app.js"), []byte("console.log(1)"), 0o600)

	s := &server.Server{Fleet: svc, Token: token, WebDir: web, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &api{t: t, srv: srv, svc: svc, model: model, token: token}
}

func (a *api) do(method, path string, body []byte, headers map[string]string) (*http.Response, string) {
	a.t.Helper()
	req, err := http.NewRequest(method, a.srv.URL+path, bytes.NewReader(body))
	if err != nil {
		a.t.Fatal(err)
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

// gql runs a GraphQL operation and returns its data, failing on any error.
func (a *api) gql(query string, vars map[string]any) map[string]any {
	a.t.Helper()
	data, errs := a.gqlRaw(query, vars)
	if len(errs) > 0 {
		a.t.Fatalf("graphql errors: %v\nquery: %s", errs, query)
	}
	return data
}

func (a *api) gqlRaw(query string, vars map[string]any) (map[string]any, []string) {
	a.t.Helper()
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	resp, raw := a.do("POST", "/graphql", body, map[string]string{"Content-Type": "application/json", "X-Roster-User": "wojtek"})
	if resp.StatusCode != 200 && resp.StatusCode != 422 {
		a.t.Fatalf("graphql: HTTP %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		Data   map[string]any `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		a.t.Fatalf("graphql: unreadable response %q: %v", raw, err)
	}
	var errs []string
	for _, e := range out.Errors {
		errs = append(errs, e.Message)
	}
	return out.Data, errs
}

func (a *api) work() {
	a.t.Helper()
	if resp, body := a.do("POST", "/internal/work?max=50", nil, nil); resp.StatusCode != 200 {
		a.t.Fatalf("work: HTTP %d: %s", resp.StatusCode, body)
	}
}

// at walks a decoded JSON document by keys and list indexes.
func at(t *testing.T, v any, path ...any) any {
	t.Helper()
	for _, step := range path {
		switch key := step.(type) {
		case string:
			obj, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("at %v: %T is not an object", step, v)
			}
			v = obj[key]
		case int:
			list, ok := v.([]any)
			if !ok || key >= len(list) {
				t.Fatalf("at %v: %T is not a list that long", step, v)
			}
			v = list[key]
		}
	}
	return v
}

func TestGraphQLFleetLifecycle(t *testing.T) {
	a := newAPI(t, "")

	// Create the agent.
	created := a.gql(`mutation($spec: String!) { applyAgent(spec: $spec) { id name status provider model triggers
		tools { name kind sideEffect requiresApproval } review { window minRuns onBreach } } }`,
		map[string]any{"spec": triageSpec})
	agent := created["applyAgent"].(map[string]any)
	if agent["name"] != "triage" || agent["status"] != "active" || agent["model"] != "primary-model" {
		t.Fatalf("applyAgent = %v", agent)
	}
	if got := at(t, agent, "triggers").([]any); len(got) != 3 || got[0] != "event ticket.created" || got[1] != "webhook" || got[2] != "schedule every 1h" {
		t.Errorf("triggers = %v", got)
	}
	if tl := at(t, agent, "tools", 0).(map[string]any); tl["kind"] != "builtin" || tl["requiresApproval"] != true || tl["sideEffect"] != true {
		t.Errorf("tool = %v", tl)
	}
	if rv := agent["review"].(map[string]any); rv["minRuns"] != float64(2) || rv["onBreach"] != "pause" {
		t.Errorf("review = %v", rv)
	}

	// An invalid spec comes back as a readable error.
	if _, errs := a.gqlRaw(`mutation { applyAgent(spec: "name: Bad Name") { id } }`, nil); len(errs) != 1 || !strings.Contains(errs[0], "is invalid") {
		t.Errorf("invalid spec errors = %v", errs)
	}

	// One right answer through an event, one wrong answer triggered by hand.
	emitted := a.gql(`mutation($p: JSON, $e: JSON) { emitEvent(name: "ticket.created", payload: $p, expected: $e) }`,
		map[string]any{"p": map[string]any{"ticket_id": "T-1"}, "e": map[string]any{"action": "reply"}})
	if emitted["emitEvent"] != float64(1) {
		t.Fatalf("emitEvent = %v", emitted["emitEvent"])
	}
	a.gql(`mutation { triggerAgent(name: "triage", input: {ticket_id: "T-2"}, expected: {action: "escalate"}) }`, nil)
	a.work()

	// The dashboard's main query.
	data := a.gql(`{
		fleet { active paused retired pendingApprovals queueDepth runs24h costUsd24h }
		agents { name stats { runs succeeded failed costUsd lastRunAt }
			scorecard { runs kpis { name grader aggregate value samples target status } } }
	}`, nil)
	// Two runs at 50% accuracy with min_runs 2: the review has already
	// paused the agent by the time the dashboard asks.
	fleetData := data["fleet"].(map[string]any)
	if fleetData["active"] != float64(0) || fleetData["paused"] != float64(1) || fleetData["runs24h"] != float64(2) || fleetData["queueDepth"] != float64(0) || fleetData["costUsd24h"].(float64) <= 0 {
		t.Errorf("fleet = %v", fleetData)
	}
	if st := at(t, data, "agents", 0, "stats").(map[string]any); st["runs"] != float64(2) || st["succeeded"] != float64(2) || st["lastRunAt"] == nil {
		t.Errorf("stats = %v", st)
	}
	accuracy := at(t, data, "agents", 0, "scorecard", "kpis", 0).(map[string]any)
	if accuracy["name"] != "accuracy" || accuracy["value"] != 0.5 || accuracy["samples"] != float64(2) || accuracy["target"] != ">= 0.75" {
		t.Errorf("accuracy = %v", accuracy)
	}
	cost := at(t, data, "agents", 0, "scorecard", "kpis", 1).(map[string]any)
	if cost["status"] != "no_target" || cost["target"] != nil {
		t.Errorf("cost = %v", cost)
	}

	detail := a.gql(`{ agent(name: "triage") { status statusReason
		events { kind actor reason }
		comparison { provider model shadow runs succeeded avgCostUsd avgSeconds scorecard { runs } }
		runs(limit: 5) { id status trigger input expected output costUsd llmCalls
			steps { seq kind name isError inputTokens costUsd output }
			scores { kpi value detail } agent { name } shadows { id } approvals { id } } } }`, nil)
	paused := detail["agent"].(map[string]any)
	if paused["status"] != "paused" || !strings.Contains(paused["statusReason"].(string), "accuracy is 0.5 over 2 runs") {
		t.Fatalf("agent after review = %v / %v", paused["status"], paused["statusReason"])
	}
	if ev := at(t, paused, "events", 0).(map[string]any); ev["kind"] != "paused" || ev["actor"] != "review" {
		t.Errorf("latest event = %v", ev)
	}
	if cmp := at(t, paused, "comparison", 0).(map[string]any); cmp["model"] != "primary-model" || cmp["shadow"] != false || cmp["runs"] != float64(2) {
		t.Errorf("comparison = %v", cmp)
	}
	run := at(t, paused, "runs", 0).(map[string]any)
	if run["status"] != "succeeded" || run["trigger"] != "manual" || at(t, run, "output", "action") != "reply" || at(t, run, "input", "ticket_id") != "T-2" {
		t.Errorf("run = %v", run)
	}
	if step := at(t, run, "steps", 0).(map[string]any); step["kind"] != "llm" || step["inputTokens"] != float64(100) || at(t, step, "output", "stop_reason") != "tool_use" {
		t.Errorf("step = %v", step)
	}
	if at(t, run, "agent", "name") != "triage" {
		t.Errorf("run.agent = %v", run["agent"])
	}

	// Late ground truth flips the wrong run to right.
	labelled := a.gql(`mutation($id: ID!) { labelRun(id: $id, expected: {action: "reply"}) { scores { kpi value } } }`, map[string]any{"id": run["id"]})
	for _, sc := range at(t, labelled, "labelRun", "scores").([]any) {
		if s := sc.(map[string]any); s["kpi"] == "accuracy" && s["value"] != float64(1) {
			t.Errorf("accuracy after labelling = %v", s["value"])
		}
	}

	// Resume, with the caller's name in the audit trail.
	resumed := a.gql(`mutation { resumeAgent(name: "triage", reason: "relabelled") { status events(limit: 1) { kind actor reason } } }`, nil)
	if ev := at(t, resumed, "resumeAgent", "events", 0).(map[string]any); ev["kind"] != "resumed" || ev["actor"] != "wojtek" || ev["reason"] != "relabelled" {
		t.Errorf("resume event = %v", ev)
	}

	// Lookups that find nothing are null, not errors.
	missing := a.gql(`{ agent(name: "nope") { id } run(id: "run_nope") { id } runs(agent: "nope") { id } }`, nil)
	if missing["agent"] != nil || missing["run"] != nil || len(missing["runs"].([]any)) != 0 {
		t.Errorf("missing = %v", missing)
	}
	if _, errs := a.gqlRaw(`mutation { pauseAgent(name: "nope", reason: "x") { id } }`, nil); len(errs) != 1 || !strings.Contains(errs[0], `no agent named "nope"`) {
		t.Errorf("pause unknown agent errors = %v", errs)
	}
}

// TestGraphQLOverGET covers the request shape browsers' GraphQL clients use
// for short queries unless told otherwise.
func TestGraphQLOverGET(t *testing.T) {
	a := newAPI(t, "")
	a.gql(`mutation($spec: String!) { applyAgent(spec: $spec) { id } }`, map[string]any{"spec": triageSpec})

	resp, body := a.do("GET", "/graphql?query="+url.QueryEscape(`query($n: String!) { agent(name: $n) { name status } }`)+
		"&variables="+url.QueryEscape(`{"n":"triage"}`), nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `"name":"triage"`) {
		t.Fatalf("query over GET: HTTP %d: %s", resp.StatusCode, body)
	}

	// A GET must never change anything.
	_, body = a.do("GET", "/graphql?query="+url.QueryEscape(`mutation { pauseAgent(name: "triage", reason: "x") { status } }`), nil, nil)
	if strings.Contains(body, `"status":"paused"`) {
		t.Fatalf("a mutation ran over GET: %s", body)
	}
	if got := a.gql(`{ agent(name: "triage") { status } }`, nil); at(t, got, "agent", "status") != "active" {
		t.Fatalf("agent status after a GET mutation attempt = %v", got)
	}
}

func TestGraphQLApprovalInbox(t *testing.T) {
	a := newAPI(t, "")
	a.gql(`mutation($spec: String!) { applyAgent(spec: $spec) { id } }`, map[string]any{"spec": triageSpec})
	a.model.Func = func(req *llm.Request) (*llm.Response, error) {
		if len(req.Messages) == 1 {
			return llmtest.Call("refund", map[string]string{"order": "O-9"}), nil
		}
		return llmtest.Call(spec.SubmitTool, map[string]string{"action": "refunded"}), nil
	}
	a.gql(`mutation { triggerAgent(name: "triage") }`, nil)
	a.work()

	inbox := a.gql(`{ approvals { id tool input status run { id status } agent { name } } fleet { pendingApprovals } }`, nil)
	if at(t, inbox, "fleet", "pendingApprovals") != float64(1) {
		t.Fatalf("pendingApprovals = %v", at(t, inbox, "fleet", "pendingApprovals"))
	}
	pending := at(t, inbox, "approvals", 0).(map[string]any)
	if pending["tool"] != "refund" || at(t, pending, "input", "order") != "O-9" || at(t, pending, "run", "status") != "awaiting_approval" || at(t, pending, "agent", "name") != "triage" {
		t.Fatalf("approval = %v", pending)
	}

	decided := a.gql(`mutation($id: ID!) { decideApproval(id: $id, approve: true, reason: "ok") { status decidedBy reason decidedAt } }`,
		map[string]any{"id": pending["id"]})
	if d := decided["decideApproval"].(map[string]any); d["status"] != "approved" || d["decidedBy"] != "wojtek" || d["decidedAt"] == nil {
		t.Errorf("decision = %v", d)
	}
	if _, errs := a.gqlRaw(`mutation($id: ID!) { decideApproval(id: $id, approve: false) { id } }`, map[string]any{"id": pending["id"]}); len(errs) != 1 || !strings.Contains(errs[0], "already been decided") {
		t.Errorf("second decision errors = %v", errs)
	}

	a.work()
	after := a.gql(`query($id: ID!) { run(id: $id) { status output approvals { status } } approvals { id } approvals2: approvals(status: "approved") { id } }`,
		map[string]any{"id": at(t, pending, "run", "id")})
	if at(t, after, "run", "status") != "succeeded" || at(t, after, "run", "output", "action") != "refunded" {
		t.Errorf("run after approval = %v", after["run"])
	}
	if len(after["approvals"].([]any)) != 0 || len(after["approvals2"].([]any)) != 1 {
		t.Errorf("inbox after approval: pending %v, approved %v", after["approvals"], after["approvals2"])
	}
}

func TestBearerTokenIsRequired(t *testing.T) {
	a := newAPI(t, "s3cret")
	protected := []struct{ method, path string }{
		{"POST", "/graphql"},
		{"POST", "/hooks/triage"},
		{"POST", "/events/pubsub"},
		{"POST", "/internal/tick"},
		{"POST", "/internal/work"},
	}
	for _, p := range protected {
		req, _ := http.NewRequest(p.method, a.srv.URL+p.path, strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: HTTP %d, want 401", p.method, p.path, resp.StatusCode)
		}
	}

	req, _ := http.NewRequest("POST", a.srv.URL+"/internal/tick", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: HTTP %d", resp.StatusCode)
	}
	if resp, _ := a.do("POST", "/internal/tick", nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("right token: HTTP %d", resp.StatusCode)
	}
	// Pub/Sub push cannot set headers, so the token may ride in the URL.
	resp, err := http.Post(a.srv.URL+"/internal/tick?token=s3cret", "application/json", nil)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Errorf("token in query: %v, %v", resp.StatusCode, err)
	}

	// The health check and the dashboard stay open.
	for _, path := range []string{"/healthz", "/"} {
		resp, err := http.Get(a.srv.URL + path)
		if err != nil || resp.StatusCode != 200 {
			t.Errorf("GET %s without a token: %v, %v", path, resp.StatusCode, err)
		}
	}
}

func TestWebhook(t *testing.T) {
	a := newAPI(t, "")
	a.gql(`mutation($spec: String!) { applyAgent(spec: $spec) { id } }`, map[string]any{"spec": triageSpec})
	a.gql(`mutation($spec: String!) { applyAgent(spec: $spec) { id } }`, map[string]any{
		"spec": "name: quiet\nmodel: {provider: scripted, name: m}\ninstructions: x\n"})

	if resp, body := a.do("POST", "/hooks/triage", []byte(`{"ticket_id":"T-5"}`), nil); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("webhook: HTTP %d: %s", resp.StatusCode, body)
	}
	a.work()
	runs := a.gql(`{ runs(agent: "triage") { trigger input status } }`, nil)
	if r := at(t, runs, "runs", 0).(map[string]any); r["trigger"] != "webhook" || at(t, r, "input", "ticket_id") != "T-5" || r["status"] != "succeeded" {
		t.Errorf("webhook run = %v", r)
	}

	cases := []struct {
		path, body string
		want       int
	}{
		{"/hooks/quiet", `{}`, http.StatusNotFound},   // no webhook trigger
		{"/hooks/missing", `{}`, http.StatusNotFound}, // no such agent
		{"/hooks/triage", `not json`, http.StatusBadRequest},
	}
	for _, c := range cases {
		if resp, _ := a.do("POST", c.path, []byte(c.body), nil); resp.StatusCode != c.want {
			t.Errorf("POST %s %q: HTTP %d, want %d", c.path, c.body, resp.StatusCode, c.want)
		}
	}

	a.gql(`mutation { pauseAgent(name: "triage", reason: "maintenance") { status } }`, nil)
	if resp, _ := a.do("POST", "/hooks/triage", []byte(`{}`), nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("webhook on a paused agent: HTTP %d, want 409", resp.StatusCode)
	}
}

func TestPubSubPush(t *testing.T) {
	a := newAPI(t, "")
	a.gql(`mutation($spec: String!) { applyAgent(spec: $spec) { id } }`, map[string]any{"spec": triageSpec})

	push := func(event, data string) int {
		msg := map[string]any{"message": map[string]any{
			"messageId":  "1",
			"data":       base64.StdEncoding.EncodeToString([]byte(data)),
			"attributes": map[string]string{"event": event},
		}}
		body, _ := json.Marshal(msg)
		resp, _ := a.do("POST", "/events/pubsub", body, nil)
		return resp.StatusCode
	}

	if code := push("ticket.created", `{"ticket_id":"T-8"}`); code != http.StatusNoContent {
		t.Fatalf("push: HTTP %d", code)
	}
	// Messages that can never succeed are acknowledged and dropped.
	if code := push("", `{}`); code != http.StatusNoContent {
		t.Errorf("push without an event: HTTP %d, want 204", code)
	}
	if code := push("ticket.created", `not json`); code != http.StatusNoContent {
		t.Errorf("push with bad data: HTTP %d, want 204", code)
	}
	if resp, _ := a.do("POST", "/events/pubsub", []byte(`garbage`), nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("unreadable push: HTTP %d, want 204", resp.StatusCode)
	}

	a.work()
	runs := a.gql(`{ runs { trigger input } }`, nil)
	list := runs["runs"].([]any)
	if len(list) != 1 || at(t, list, 0, "trigger") != "event:ticket.created" || at(t, list, 0, "input", "ticket_id") != "T-8" {
		t.Errorf("runs = %v, want exactly the one valid message", list)
	}
}

func TestDashboardFiles(t *testing.T) {
	a := newAPI(t, "")
	tests := []struct {
		path     string
		status   int
		contains string
	}{
		{"/", 200, "dashboard"},
		{"/assets/app.js", 200, "console.log"},
		{"/agents/triage", 200, "dashboard"}, // client-side route
		{"/assets/missing.js", 404, ""},
		{"/../../etc/passwd", 200, "dashboard"}, // cleaned to a client-side route
	}
	for _, tt := range tests {
		resp, body := a.do("GET", tt.path, nil, nil)
		if resp.StatusCode != tt.status || !strings.Contains(body, tt.contains) {
			t.Errorf("GET %s = %d %q, want %d containing %q", tt.path, resp.StatusCode, body, tt.status, tt.contains)
		}
	}
	if resp, body := a.do("GET", "/healthz", nil, nil); resp.StatusCode != 200 || body != "ok\n" {
		t.Errorf("healthz = %d %q", resp.StatusCode, body)
	}
}

func TestWorkValidatesItsLimit(t *testing.T) {
	a := newAPI(t, "")
	for _, q := range []string{"max=0", "max=1000", "max=abc"} {
		if resp, _ := a.do("POST", "/internal/work?"+q, nil, nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("?%s: HTTP %d, want 400", q, resp.StatusCode)
		}
	}
	resp, body := a.do("POST", "/internal/work", nil, nil)
	if resp.StatusCode != 200 || strings.TrimSpace(body) != `{"worked":0}` {
		t.Errorf("work on an empty queue = %d %q", resp.StatusCode, body)
	}
}
