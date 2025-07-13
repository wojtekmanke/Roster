// Package pricing turns token usage into dollars.
package pricing

import (
	_ "embed"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/wojtekmanke/roster/internal/llm"
)

//go:embed pricing.yaml
var defaultYAML []byte

// Price is the cost of one million tokens of each kind, in USD.
type Price struct {
	Input      float64 `yaml:"input"`
	Output     float64 `yaml:"output"`
	CacheRead  float64 `yaml:"cache_read"`
	CacheWrite float64 `yaml:"cache_write"`
}

// Table maps provider, then model, to a Price.
type Table map[string]map[string]Price

// Default returns the built-in price table.
func Default() Table {
	t, err := Parse(defaultYAML)
	if err != nil {
		panic("pricing: built-in table is invalid: " + err.Error())
	}
	return t
}

// Parse reads a price table from YAML.
func Parse(data []byte) (Table, error) {
	var t Table
	if err := yaml.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("pricing: %w", err)
	}
	for provider, models := range t {
		for model, p := range models {
			if p.Input < 0 || p.Output < 0 || p.CacheRead < 0 || p.CacheWrite < 0 {
				return nil, fmt.Errorf("pricing: %s/%s has a negative price", provider, model)
			}
		}
	}
	return t, nil
}

// Load returns the built-in table with entries from the YAML file at path
// layered on top. An empty path returns the built-in table unchanged.
func Load(path string) (Table, error) {
	t := Default()
	if path == "" {
		return t, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("pricing: %w", err)
	}
	overrides, err := Parse(data)
	if err != nil {
		return nil, err
	}
	for provider, models := range overrides {
		if t[provider] == nil {
			t[provider] = map[string]Price{}
		}
		for model, p := range models {
			t[provider][model] = p
		}
	}
	return t, nil
}

// Lookup finds the price for a model, falling back to the provider's "*" entry.
func (t Table) Lookup(provider, model string) (Price, bool) {
	models := t[provider]
	if p, ok := models[model]; ok {
		return p, true
	}
	p, ok := models["*"]
	return p, ok
}

// Cost prices the usage of one call. served is the model that answered and
// requested the model that was asked for; they differ when a fallback served
// the call. The second result is false when neither model has a price, in
// which case the cost is reported as zero.
func (t Table) Cost(provider, served, requested string, u llm.Usage) (float64, bool) {
	p, ok := t.Lookup(provider, served)
	if !ok {
		p, ok = t.Lookup(provider, requested)
	}
	if !ok {
		return 0, false
	}
	const perToken = 1e-6
	usd := float64(u.InputTokens)*p.Input +
		float64(u.OutputTokens)*p.Output +
		float64(u.CacheReadTokens)*p.CacheRead +
		float64(u.CacheWriteTokens)*p.CacheWrite
	return usd * perToken, true
}
