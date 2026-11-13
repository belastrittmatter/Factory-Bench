// Package pricing estimates cost from FactoryBench's own versioned price tables (prices/<YYYY-MM-DD>.json).
package pricing

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
)

//go:embed prices/*.json
var files embed.FS

type table struct {
	Models map[string]struct {
		Input      float64  `json:"input"`
		Output     float64  `json:"output"`
		CacheRead  *float64 `json:"cache_read"`
		CacheWrite *float64 `json:"cache_write"`
	} `json:"models"`
}

type version struct {
	name string // YYYY-MM-DD, the date the table takes effect
	t    table
}

// versions holds every price table, oldest first.
var versions = load()

func load() []version {
	entries, err := files.ReadDir("prices")
	if err != nil || len(entries) == 0 {
		panic(fmt.Sprintf("pricing: no price tables: %v", err))
	}
	var vs []version
	for _, e := range entries { // ReadDir sorts by name; files are YYYY-MM-DD.json so that is date order
		b, err := files.ReadFile(path.Join("prices", e.Name()))
		if err != nil {
			panic("pricing: " + err.Error())
		}
		v := version{name: strings.TrimSuffix(e.Name(), ".json")}
		if err := json.Unmarshal(b, &v.t); err != nil {
			panic("pricing: " + e.Name() + ": " + err.Error())
		}
		vs = append(vs, v)
	}
	return vs
}

// at returns the table in effect at ts (RFC 3339): the newest one dated on or before ts's day.
// A ts before the first table gets the first table, the earliest prices known.
func at(ts string) version {
	v := versions[0]
	for _, c := range versions[1:] {
		if c.name <= ts[:min(len(ts), 10)] {
			v = c
		}
	}
	return v
}

var thinkingLevel = regexp.MustCompile(`-(minimal|low|medium|high)$`)

// Estimate prices one session at the prices in effect at ts and returns the price version used.
// ok is false when the model, or a token class it used, has no price.
func Estimate(ts, model string, input, output int64, cacheRead, cacheWrite *int64) (usd float64, priceVersion string, ok bool) {
	v := at(ts)
	m, found := v.t.Models[model]
	if !found { // agents like Antigravity append the thinking level, which doesn't change the per-token price
		m, found = v.t.Models[thinkingLevel.ReplaceAllString(model, "")]
	}
	if !found {
		return 0, "", false
	}
	usd = float64(input)*m.Input + float64(output)*m.Output
	for _, c := range []struct {
		n *int64
		p *float64
	}{{cacheRead, m.CacheRead}, {cacheWrite, m.CacheWrite}} {
		if c.n == nil || *c.n == 0 {
			continue
		}
		if c.p == nil {
			return 0, "", false
		}
		usd += float64(*c.n) * *c.p
	}
	return usd / 1e6, v.name, true
}
