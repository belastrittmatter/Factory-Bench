// Package pricing estimates cost from FactoryBench's own versioned price tables (prices/<YYYY-MM-DD>.json).
package pricing

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
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

// Version is the newest price table; estimates always use it and record its name.
var Version, current = load()

func load() (string, table) {
	entries, err := files.ReadDir("prices")
	if err != nil || len(entries) == 0 {
		panic(fmt.Sprintf("pricing: no price tables: %v", err))
	}
	name := entries[len(entries)-1].Name() // ReadDir sorts by name; files are YYYY-MM-DD.json so that is date order
	b, err := files.ReadFile(path.Join("prices", name))
	if err != nil {
		panic("pricing: " + err.Error())
	}
	var t table
	if err := json.Unmarshal(b, &t); err != nil {
		panic("pricing: " + name + ": " + err.Error())
	}
	return strings.TrimSuffix(name, ".json"), t
}

// Estimate prices one session. ok is false when the model, or a token class it used, has no price.
func Estimate(model string, input, output int64, cacheRead, cacheWrite *int64) (usd float64, ok bool) {
	m, found := current.Models[model]
	if !found {
		return 0, false
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
			return 0, false
		}
		usd += float64(*c.n) * *c.p
	}
	return usd / 1e6, true
}
