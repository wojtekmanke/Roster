// Package cli implements the roster command.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/wojtekmanke/roster/internal/agent"
	"github.com/wojtekmanke/roster/internal/demo"
	"github.com/wojtekmanke/roster/internal/fleet"
	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/llm/anthropic"
	"github.com/wojtekmanke/roster/internal/llm/openaicompat"
	"github.com/wojtekmanke/roster/internal/pricing"
	"github.com/wojtekmanke/roster/internal/scoring"
	"github.com/wojtekmanke/roster/internal/store"
	"github.com/wojtekmanke/roster/internal/tool"
)

const usage = `Roster builds, runs and scores AI agents.

Usage:
  roster serve                       Run the API, dashboard, workers and scheduler
  roster migrate                     Apply database migrations
  roster agents validate FILE...     Check agent specs without storing them
  roster agents apply FILE...        Create or update agents from spec files
  roster agents apply --examples     Create or update the bundled example agents
                      --demo         ...on scripted stand-in models, no API key needed
  roster shop simulate [flags]       Generate demo-store scenarios and emit their events
  roster work [--approve]            Execute queued runs until the queue is empty, then exit
  roster report                      Print every agent's scorecard as Markdown
  roster version

Environment:
  DATABASE_URL          Postgres connection string (required except for validate)
  PORT                  HTTP port for serve (default 8080)
  ROSTER_API_TOKEN      Bearer token required by the API; unset means open
  ROSTER_WORKERS        Jobs executed at once by serve (default 4, 0 disables)
  ROSTER_WEB_DIR        Built dashboard to serve (default web/dist when present)
  ROSTER_DEMO_SHOP      "true" mounts the demo store's API at /shop/graphql
  ROSTER_PRICING_FILE   YAML file of token prices layered over the built-in table
  ANTHROPIC_API_KEY, OPENAI_API_KEY, XAI_API_KEY, OLLAMA_BASE_URL
                        Model provider credentials
`

// Env is everything the command needs from its surroundings, so tests can
// run it without a real process environment.
type Env struct {
	Args   []string
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	// Providers overrides the model providers. Nil means build them from the
	// environment.
	Providers llm.Registry
	// Version is printed by `roster version`.
	Version string
}

// Run executes the command and returns its exit status.
func Run(ctx context.Context, env Env) int {
	if env.Getenv == nil {
		env.Getenv = os.Getenv
	}
	if len(env.Args) == 0 {
		fmt.Fprint(env.Stderr, usage)
		return 2
	}
	var err error
	switch cmd, args := env.Args[0], env.Args[1:]; cmd {
	case "serve":
		err = serve(ctx, env)
	case "migrate":
		err = migrate(ctx, env)
	case "agents":
		err = agentsCmd(ctx, env, args)
	case "shop":
		err = shopCmd(ctx, env, args)
	case "work":
		err = work(ctx, env, args)
	case "report":
		err = report(ctx, env)
	case "version":
		fmt.Fprintln(env.Stdout, env.Version)
	case "help", "-h", "--help":
		fmt.Fprint(env.Stdout, usage)
	default:
		fmt.Fprintf(env.Stderr, "roster: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(env.Stderr, "roster:", err)
		return 1
	}
	return 0
}

func (e Env) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(e.Stderr, nil))
}

// openStore connects to the database named by DATABASE_URL.
func (e Env) openStore(ctx context.Context) (*store.Store, error) {
	url := e.Getenv("DATABASE_URL")
	if url == "" {
		return nil, errors.New("DATABASE_URL is not set")
	}
	return store.Open(ctx, url)
}

// providers builds the model providers from the environment.
//
// Claude is always registered: the SDK finds credentials in several places
// besides ANTHROPIC_API_KEY. Ollama is always registered because it needs no
// key. OpenAI and xAI appear only when their keys are set, so a spec that
// names them fails with "provider is not configured" rather than a 401.
func (e Env) providers() llm.Registry {
	if e.Providers != nil {
		return e.Providers
	}
	reg := llm.Registry{
		anthropic.Name:      anthropic.New(),
		openaicompat.Ollama: openaicompat.NewOllama(e.Getenv("OLLAMA_BASE_URL")),
		// Scripted stand-ins, used only by specs applied with --demo.
		demo.Name: demo.Provider{},
	}
	if e.Getenv("OPENAI_API_KEY") != "" {
		reg[openaicompat.OpenAI] = openaicompat.NewOpenAI()
	}
	if key := e.Getenv("XAI_API_KEY"); key != "" {
		reg[openaicompat.XAI] = openaicompat.NewXAI(key)
	}
	return reg
}

// service assembles the control plane around a store.
func (e Env) service(db *store.Store, getenv func(string) string) (*fleet.Service, error) {
	prices, err := pricing.Load(e.Getenv("ROSTER_PRICING_FILE"))
	if err != nil {
		return nil, err
	}
	providers := e.providers()
	graders := scoring.Builtin()
	graders["judge"] = scoring.Judge{Providers: providers}
	return &fleet.Service{
		Store:   db,
		Runner:  &agent.Runner{Providers: providers, Pricing: prices, Recorder: db},
		Tools:   &tool.Builder{Getenv: getenv},
		Graders: graders,
		Log:     e.logger(),
	}, nil
}

func migrate(ctx context.Context, env Env) error {
	db, err := env.openStore(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	applied, err := db.Migrate(ctx)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		fmt.Fprintln(env.Stdout, "database is up to date")
		return nil
	}
	fmt.Fprintf(env.Stdout, "applied %s\n", strings.Join(applied, ", "))
	return nil
}
