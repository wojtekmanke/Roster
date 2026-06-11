package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wojtekmanke/roster/agents"
	"github.com/wojtekmanke/roster/internal/fleet"
	"github.com/wojtekmanke/roster/internal/scoring"
	"github.com/wojtekmanke/roster/internal/server"
	"github.com/wojtekmanke/roster/internal/shop"
	shopgraph "github.com/wojtekmanke/roster/internal/shop/graph"
	"github.com/wojtekmanke/roster/internal/spec"
)

// shutdownGrace is how long serve waits for runs in flight when asked to
// stop. Runs still executing after that are failed as interrupted on the
// next start.
const shutdownGrace = 25 * time.Second

func serve(ctx context.Context, env Env) error {
	log := env.logger()
	db, err := env.openStore(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	if applied, err := db.Migrate(ctx); err != nil {
		return err
	} else if len(applied) > 0 {
		log.Info("applied migrations", "migrations", applied)
	}

	port := env.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	demoShop := env.Getenv("ROSTER_DEMO_SHOP") == "true"

	// With the demo store mounted, its API lives in this process, so the
	// example agents can find it without any configuration.
	getenv := env.Getenv
	if demoShop {
		getenv = func(key string) string {
			if v := env.Getenv(key); v != "" {
				return v
			}
			switch key {
			case "SHOP_GRAPHQL_URL":
				return "http://127.0.0.1:" + port + "/shop/graphql"
			case "SHOP_API_TOKEN":
				return "demo"
			}
			return ""
		}
	}
	svc, err := env.service(db, getenv)
	if err != nil {
		return err
	}

	webDir := env.Getenv("ROSTER_WEB_DIR")
	if webDir == "" {
		if _, err := os.Stat("web/dist/index.html"); err == nil {
			webDir = "web/dist"
		}
	}
	srv := &server.Server{Fleet: svc, Token: env.Getenv("ROSTER_API_TOKEN"), WebDir: webDir, Log: log}
	if srv.Token == "" {
		log.Warn("ROSTER_API_TOKEN is not set: the API is open to anyone who can reach it")
	}
	if demoShop {
		srv.Mounts = map[string]http.Handler{
			"/shop/graphql": shopgraph.NewHandler(&shop.Shop{Pool: db.Pool}, getenv("SHOP_API_TOKEN")),
		}
	}

	workers := 4
	if raw := env.Getenv("ROSTER_WORKERS"); raw != "" {
		if workers, err = strconv.Atoi(raw); err != nil || workers < 0 {
			return fmt.Errorf("ROSTER_WORKERS must be a non-negative integer, got %q", raw)
		}
	}

	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return err
	}
	httpServer := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	workersDone := make(chan struct{})
	go func() {
		defer close(workersDone)
		if workers > 0 {
			host, _ := os.Hostname()
			svc.Run(ctx, fleet.WorkerOptions{Concurrency: workers, ID: host})
		}
	}()
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()
	log.Info("roster is listening", "addr", listener.Addr().String(), "workers", workers,
		"dashboard", webDir != "", "demo_shop", demoShop, "providers", svc.Runner.Providers.Names())

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down", "grace", shutdownGrace.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown", "error", err)
	}
	select {
	case <-workersDone:
	case <-shutdownCtx.Done():
		log.Warn("stopped with runs still in flight; they will be failed as interrupted")
	}
	return nil
}

func agentsCmd(ctx context.Context, env Env, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: roster agents validate|apply FILE...")
	}
	switch args[0] {
	case "validate":
		files, err := readSpecs(args[1:], false)
		if err != nil {
			return err
		}
		failed := 0
		for _, f := range files {
			if _, err := spec.Parse(f.data); err != nil {
				failed++
				fmt.Fprintf(env.Stdout, "FAIL %s\n%v\n", f.name, err)
				continue
			}
			fmt.Fprintf(env.Stdout, "ok   %s\n", f.name)
		}
		if failed > 0 {
			return fmt.Errorf("%d of %d specs are invalid", failed, len(files))
		}
		return nil

	case "apply":
		fs := flag.NewFlagSet("roster agents apply", flag.ContinueOnError)
		fs.SetOutput(env.Stderr)
		examples := fs.Bool("examples", false, "apply the bundled example agents")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		files, err := readSpecs(fs.Args(), *examples)
		if err != nil {
			return err
		}
		db, err := env.openStore(ctx)
		if err != nil {
			return err
		}
		defer db.Close()
		svc, err := env.service(db, env.Getenv)
		if err != nil {
			return err
		}
		for _, f := range files {
			a, changed, err := svc.Apply(ctx, f.data, "cli")
			if err != nil {
				return fmt.Errorf("%s: %w", f.name, err)
			}
			verb := "unchanged"
			if changed {
				verb = "applied"
			}
			fmt.Fprintf(env.Stdout, "%-9s %s (%s/%s)\n", verb, a.Name, a.Provider, a.Model)
		}
		return nil
	}
	return fmt.Errorf("unknown agents command %q", args[0])
}

type specFile struct {
	name string
	data []byte
}

func readSpecs(paths []string, examples bool) ([]specFile, error) {
	var files []specFile
	if examples {
		bundled, err := agents.Examples()
		if err != nil {
			return nil, err
		}
		for name, data := range bundled {
			files = append(files, specFile{name, data})
		}
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		files = append(files, specFile{path, data})
	}
	if len(files) == 0 {
		return nil, errors.New("no spec files given")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files, nil
}

func shopCmd(ctx context.Context, env Env, args []string) error {
	if len(args) == 0 || args[0] != "simulate" {
		return errors.New("usage: roster shop simulate [flags]")
	}
	fs := flag.NewFlagSet("roster shop simulate", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	var c shop.Counts
	fs.IntVar(&c.Tickets, "tickets", 10, "support tickets to create")
	fs.IntVar(&c.Products, "products", 10, "products to create without attributes")
	fs.IntVar(&c.Restocks, "restocks", 10, "low-stock alerts to raise")
	fs.IntVar(&c.Orders, "orders", 10, "orders to place")
	seed := fs.Uint64("seed", 1, "random seed; the same seed gives the same scenarios")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	db, err := env.openStore(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	svc, err := env.service(db, env.Getenv)
	if err != nil {
		return err
	}
	scenarios, err := (&shop.Shop{Pool: db.Pool}).Generate(ctx, *seed, c)
	if err != nil {
		return err
	}
	queued, unheard := 0, map[string]bool{}
	for _, sc := range scenarios {
		n, err := svc.Emit(ctx, sc.Event, sc.Payload, sc.Expected)
		if err != nil {
			return err
		}
		if n == 0 {
			unheard[sc.Event] = true
		}
		queued += n
	}
	fmt.Fprintf(env.Stdout, "generated %d scenarios, queued %d runs\n", len(scenarios), queued)
	for event := range unheard {
		fmt.Fprintf(env.Stdout, "no active agent listens for %s (try `roster agents apply --examples`)\n", event)
	}
	return nil
}

// report prints every agent's scorecard and model comparison as Markdown.
func report(ctx context.Context, env Env) error {
	db, err := env.openStore(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	svc, err := env.service(db, env.Getenv)
	if err != nil {
		return err
	}
	list, err := db.Agents(ctx)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(env.Stdout, "No agents yet.")
		return nil
	}

	w := env.Stdout
	for i := range list {
		a := &list[i]
		sp, err := a.Spec()
		if err != nil {
			return fmt.Errorf("%s: %w", a.Name, err)
		}
		models, err := svc.Comparison(ctx, a, sp)
		if err != nil {
			return err
		}

		fmt.Fprintf(w, "## %s\n\n", a.Name)
		status := a.Status
		if a.StatusReason != "" {
			status += ": " + a.StatusReason
		}
		fmt.Fprintf(w, "Status: %s\n\n", status)
		if len(models) == 0 {
			fmt.Fprintf(w, "No finished runs in the current review window.\n\n")
			continue
		}

		header := []string{"Model", "Role", "Runs", "Succeeded", "Avg cost", "Avg time"}
		for _, kpi := range sp.KPIs {
			label := kpi.Name
			if kpi.Target != nil {
				label += " (" + kpi.Target.String() + ")"
			}
			header = append(header, label)
		}
		fmt.Fprintf(w, "| %s |\n|%s\n", strings.Join(header, " | "), strings.Repeat(" --- |", len(header)))
		for _, m := range models {
			role := "primary"
			if m.Shadow {
				role = "shadow"
			}
			row := []string{
				m.Provider + "/" + m.Model, role,
				strconv.Itoa(m.Runs), strconv.Itoa(m.Succeeded),
				fmt.Sprintf("$%.4f", m.AvgCost), fmt.Sprintf("%.1fs", m.AvgMS/1000),
			}
			for _, kpi := range m.Scorecard.KPIs {
				row = append(row, formatKPI(kpi))
			}
			fmt.Fprintf(w, "| %s |\n", strings.Join(row, " | "))
		}
		fmt.Fprintln(w)
	}
	return nil
}

func formatKPI(k scoring.KPIResult) string {
	if k.Samples == 0 {
		return "n/a"
	}
	cell := fmt.Sprintf("%s (n=%d)", strconv.FormatFloat(math.Round(k.Value*10000)/10000, 'f', -1, 64), k.Samples)
	switch k.Status {
	case scoring.StatusBreach:
		cell += " MISSED"
	case scoring.StatusInsufficient:
		cell += " too few"
	}
	return cell
}
