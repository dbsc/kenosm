// Package pricing maps model names to per-token prices and computes the USD
// cost of a single API call. Prices are per one million tokens.
package pricing

import (
	"encoding/json"
	"strings"
)

// Price is the cost per one million tokens, in USD. Thought (thinking) tokens
// default to the output price when zero.
type Price struct {
	Input   float64 `json:"input"`
	Output  float64 `json:"output"`
	Thought float64 `json:"thought,omitempty"`
}

// Table maps a model name (exact match) to its price. Unknown models fall back
// to pattern rules (pro / lite / flash tiers).
type Table map[string]Price

// DefaultTable returns the built-in price list for the models kenosm ships with.
// Exact entries win; anything else is priced by name pattern in Lookup.
func DefaultTable() Table {
	return Table{
		"gemini-3.8-flash":      {Input: 0.15, Output: 0.60},
		"gemini-3.7-flash":      {Input: 0.15, Output: 0.60},
		"gemini-3.6-flash":      {Input: 0.15, Output: 0.60},
		"gemini-3.5-flash":      {Input: 0.15, Output: 0.60},
		"gemini-3.5-flash-lite": {Input: 0.075, Output: 0.30},
		"gemini-flash-latest":   {Input: 0.15, Output: 0.60},
	}
}

// ParseOverrides decodes a JSON map of model -> Price, e.g.
// {"gemini-3.8-flash":{"input":0.15,"output":0.6,"thought":0.6}}.
func ParseOverrides(s string) (Table, error) {
	var m map[string]Price
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return Table(m), nil
}

// Lookup returns the price for a model. Exact matches come from the table;
// unknown names fall back to a tier guessed from the name: "pro" models are the
// most expensive, "lite" models the cheapest, everything else the flash tier.
func (t Table) Lookup(model string) Price {
	m := strings.TrimSpace(model)
	if p, ok := t[m]; ok {
		return normalize(p)
	}
	lower := strings.ToLower(m)
	switch {
	case strings.Contains(lower, "pro"):
		return normalize(Price{Input: 1.25, Output: 10.0})
	case strings.Contains(lower, "lite"):
		return normalize(Price{Input: 0.075, Output: 0.30})
	default:
		return normalize(Price{Input: 0.15, Output: 0.60})
	}
}

// Cost computes the USD cost of one call: input tokens, output tokens and
// thinking tokens are each billed at their own per-1M rate.
func (t Table) Cost(model string, prompt, completion, thought int) float64 {
	p := t.Lookup(model)
	in := float64(prompt) / 1e6 * p.Input
	out := float64(completion) / 1e6 * p.Output
	think := float64(thought) / 1e6 * p.Thought
	return in + out + think
}

func normalize(p Price) Price {
	if p.Thought <= 0 {
		p.Thought = p.Output
	}
	return p
}
