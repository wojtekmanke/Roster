package pricing_test

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/pricing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCost(t *testing.T) {
	table := pricing.Default()
	usage := llm.Usage{InputTokens: 1_000_000, OutputTokens: 100_000, CacheReadTokens: 2_000_000, CacheWriteTokens: 400_000}

	tests := []struct {
		name              string
		provider          string
		served, requested string
		want              float64
		wantPriced        bool
	}{
		// 1M*4 + 0.1M*20 + 2M*0.20 + 0.4M*5 = 4 + 2 + 0.4 + 2
		{"exact", "anthropic", "claude-opus-5-5", "claude-opus-5-5", 8.4, true},
		// A fallback served the call: bill at the served model's price.
		{"served model wins", "anthropic", "claude-opus-5", "claude-opus-5-5", 5 + 2.5 + 1 + 2.5, true},
		// The API echoed a snapshot name we have no price for.
		{"falls back to requested", "openai", "gpt-6.1-sol-2026-08-01", "gpt-6.1-sol", 2 + 1 + 0.2, true},
		{"provider wildcard", "ollama", "llama3.2", "llama3.2", 0, true},
		{"unknown model", "openai", "gpt-unknown", "gpt-unknown", 0, false},
		{"unknown provider", "acme", "m", "m", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, priced := table.Cost(tt.provider, tt.served, tt.requested, usage)
			if priced != tt.wantPriced || !near(got, tt.want) {
				t.Fatalf("Cost = %v, %v; want %v, %v", got, priced, tt.want, tt.wantPriced)
			}
		})
	}
}

func TestLoadLayersOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pricing.yaml")
	override := "openai:\n  gpt-6.1-sol: {input: 1, output: 2}\nacme:\n  rocket: {input: 3, output: 4}\n"
	if err := os.WriteFile(path, []byte(override), 0o600); err != nil {
		t.Fatal(err)
	}
	table, err := pricing.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := table.Lookup("openai", "gpt-6.1-sol"); p.Input != 1 || p.Output != 2 {
		t.Errorf("override not applied: %+v", p)
	}
	if p, ok := table.Lookup("acme", "rocket"); !ok || p.Input != 3 {
		t.Errorf("new provider not added: %+v, %v", p, ok)
	}
	if _, ok := table.Lookup("anthropic", "claude-opus-5-5"); !ok {
		t.Error("built-in entries were lost")
	}
}

func TestParseRejectsNegativePrices(t *testing.T) {
	if _, err := pricing.Parse([]byte("p:\n  m: {input: -1, output: 1}\n")); err == nil {
		t.Fatal("expected an error")
	}
}
