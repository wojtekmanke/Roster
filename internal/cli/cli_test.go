package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wojtekmanke/roster/internal/cli"
	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/llm/llmtest"
	"github.com/wojtekmanke/roster/internal/spec"
	"github.com/wojtekmanke/roster/internal/store/storetest"
)

// run executes the CLI with the given environment and returns its exit
// status and what it printed.
func run(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	model := llmtest.NewScripted()
	model.Func = func(*llm.Request) (*llm.Response, error) {
		return llmtest.Call(spec.SubmitTool, map[string]any{"flag": false, "signals": []string{}}), nil
	}
	code := cli.Run(context.Background(), cli.Env{
		Args:      args,
		Stdout:    &stdout,
		Stderr:    &stderr,
		Getenv:    func(k string) string { return env[k] },
		Providers: llm.Registry{"anthropic": model},
		Version:   "test",
	})
	return code, stdout.String(), stderr.String()
}

func TestUsageAndVersion(t *testing.T) {
	if code, _, stderr := run(t, nil); code != 2 || !strings.Contains(stderr, "Usage:") {
		t.Errorf("no arguments: code %d, stderr %q", code, stderr)
	}
	if code, _, stderr := run(t, nil, "frobnicate"); code != 2 || !strings.Contains(stderr, `unknown command "frobnicate"`) {
		t.Errorf("unknown command: code %d, stderr %q", code, stderr)
	}
	if code, stdout, _ := run(t, nil, "version"); code != 0 || stdout != "test\n" {
		t.Errorf("version: code %d, stdout %q", code, stdout)
	}
	if code, stdout, _ := run(t, nil, "help"); code != 0 || !strings.Contains(stdout, "roster serve") {
		t.Errorf("help: code %d", code)
	}
	if code, _, stderr := run(t, nil, "migrate"); code != 1 || !strings.Contains(stderr, "DATABASE_URL is not set") {
		t.Errorf("migrate without a database: code %d, stderr %q", code, stderr)
	}
}

func TestAgentsValidate(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yaml")
	bad := filepath.Join(dir, "bad.yaml")
	os.WriteFile(good, []byte("name: good\nmodel: {provider: ollama, name: m}\ninstructions: x\n"), 0o600)
	os.WriteFile(bad, []byte("name: Bad Name\n"), 0o600)

	if code, stdout, _ := run(t, nil, "agents", "validate", good); code != 0 || !strings.Contains(stdout, "ok   "+good) {
		t.Errorf("valid spec: code %d, stdout %q", code, stdout)
	}
	code, stdout, stderr := run(t, nil, "agents", "validate", good, bad)
	if code != 1 || !strings.Contains(stdout, "FAIL "+bad) || !strings.Contains(stderr, "1 of 2 specs are invalid") {
		t.Errorf("invalid spec: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, _, _ := run(t, nil, "agents", "validate"); code != 1 {
		t.Errorf("no files: code %d", code)
	}
}

func TestApplySimulateAndReport(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	env := map[string]string{"DATABASE_URL": url}

	if code, stdout, _ := run(t, env, "migrate"); code != 0 || stdout != "database is up to date\n" {
		t.Fatalf("migrate: code %d, stdout %q", code, stdout)
	}
	if code, stdout, _ := run(t, env, "report"); code != 0 || stdout != "No agents yet.\n" {
		t.Fatalf("report on an empty fleet: code %d, stdout %q", code, stdout)
	}

	// Before any agent exists, nothing listens for the events.
	code, stdout, stderr := run(t, env, "shop", "simulate", "--tickets", "1", "--products", "0", "--restocks", "0", "--orders", "0")
	if code != 0 || !strings.Contains(stdout, "queued 0 runs") || !strings.Contains(stdout, "no active agent listens for ticket.created") {
		t.Fatalf("simulate with no agents: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	code, stdout, stderr = run(t, env, "agents", "apply", "--examples")
	if code != 0 || strings.Count(stdout, "applied") != 5 || !strings.Contains(stdout, "fraud-screen (anthropic/claude-opus-5-5)") {
		t.Fatalf("apply --examples: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, stdout, _ = run(t, env, "agents", "apply", "--examples"); strings.Count(stdout, "unchanged") != 5 {
		t.Errorf("second apply: stdout %q, want everything unchanged", stdout)
	}

	code, stdout, stderr = run(t, env, "shop", "simulate", "--tickets", "2", "--products", "2", "--restocks", "2", "--orders", "3", "--seed", "9")
	if code != 0 || stdout != "generated 9 scenarios, queued 9 runs\n" {
		t.Fatalf("simulate: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	code, stdout, stderr = run(t, env, "report")
	if code != 0 || !strings.Contains(stdout, "## support-triage") || !strings.Contains(stdout, "No finished runs in the current review window.") {
		t.Fatalf("report: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestServe(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	web := t.TempDir()
	os.WriteFile(filepath.Join(web, "index.html"), []byte("<html>roster</html>"), 0o600)

	// Find a free port.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	env := map[string]string{
		"DATABASE_URL":     url,
		"PORT":             fmt.Sprint(port),
		"ROSTER_API_TOKEN": "tok",
		"ROSTER_WEB_DIR":   web,
		"ROSTER_DEMO_SHOP": "true",
		"ROSTER_WORKERS":   "2",
	}
	run(t, map[string]string{"DATABASE_URL": url}, "agents", "apply", "--examples")
	run(t, map[string]string{"DATABASE_URL": url}, "shop", "simulate", "--tickets", "0", "--products", "0", "--restocks", "0", "--orders", "3")

	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr bytes.Buffer
	exited := make(chan int, 1)
	model := llmtest.NewScripted()
	// The fraud screen fetches the order through the demo store's API, which
	// is mounted in this same process, then submits.
	model.Func = func(req *llm.Request) (*llm.Response, error) {
		if len(req.Messages) == 1 {
			id := strings.TrimSuffix(strings.TrimPrefix(req.Messages[0].Text(), "Screen order "), ".")
			return llmtest.Call("get_order", map[string]string{"id": id}), nil
		}
		if result := req.Messages[2].Content[0]; result.IsError || !strings.Contains(result.Text, "billingCountry") {
			return nil, fmt.Errorf("get_order did not reach the demo store: %s", result.Text)
		}
		return llmtest.Call(spec.SubmitTool, map[string]any{"flag": false, "signals": []string{}}), nil
	}
	go func() {
		exited <- cli.Run(ctx, cli.Env{
			Args: []string{"serve"}, Stdout: &stdout, Stderr: &stderr,
			Getenv:    func(k string) string { return env[k] },
			Providers: llm.Registry{"anthropic": model},
		})
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	get := func(path, token string) (int, string) {
		req, _ := http.NewRequest("GET", base+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		var body bytes.Buffer
		body.ReadFrom(resp.Body)
		return resp.StatusCode, body.String()
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		if code, _ := get("/healthz", ""); code == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never became healthy; stderr:\n%s", stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code, body := get("/", ""); code != 200 || !strings.Contains(body, "roster") {
		t.Errorf("dashboard: %d %q", code, body)
	}

	// The in-process workers pick up the queued runs and finish them using
	// the demo store mounted at /shop/graphql.
	query := `{"query":"{ runs { status error } }"}`
	finished := func() (int, string) {
		req, _ := http.NewRequest("POST", base+"/graphql", strings.NewReader(query))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer tok")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		var body bytes.Buffer
		body.ReadFrom(resp.Body)
		return strings.Count(body.String(), `"status":"succeeded"`), body.String()
	}
	for {
		n, body := finished()
		if n == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of 3 runs succeeded: %s\nstderr:\n%s", n, body, stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	select {
	case code := <-exited:
		if code != 0 {
			t.Errorf("serve exited with %d; stderr:\n%s", code, stderr.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("serve did not shut down")
	}
	if !strings.Contains(stderr.String(), "roster is listening") || !strings.Contains(stderr.String(), "shutting down") {
		t.Errorf("log is missing start or stop lines:\n%s", stderr.String())
	}
}
