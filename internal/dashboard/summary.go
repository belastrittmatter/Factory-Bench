package dashboard

import (
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"time"

	"factorybench/internal/report"
)

// summary is the Overview's issue funnel, time to PR, cost per merged issue and rework. Nil = nothing to measure.
type summary struct {
	Issues, WithPR, Merged int
	AvgToPRSeconds         *float64
	StagedToPR             int // issue rows in AvgToPRSeconds from staged runs: an early stage opened their PR
	CostPerMerged          *float64
	CostLowerBound         bool // some cost unpriced or missing: CostPerMerged is a lower bound
	IssuesWithDispatches   int
	Bounces, Redispatches  int
	ReworkPerIssue         *float64 // bounces per issue that has dispatches
}

// summarize aggregates report.ItemRows (one row per issue and factory) by issue. lanes holds the dispatched lanes of
// each issue ("repo#n") in order; a nil map means no run manifests, so rework stays unknown.
func summarize(rows []report.ItemRow, lanes map[string][]string) summary {
	var s summary
	pr, merged := map[string]bool{}, map[string]bool{}
	seen := map[string]bool{}
	cost, priced, toPR, nToPR := 0.0, false, 0.0, 0
	for _, r := range rows {
		seen[r.Item] = true
		pr[r.Item] = pr[r.Item] || r.HasPR
		merged[r.Item] = merged[r.Item] || r.Merged
		if r.CostUSD != nil {
			cost, priced = cost+*r.CostUSD, true
		}
		s.CostLowerBound = s.CostLowerBound || r.CostLowerBound || r.CostUSD == nil
		if r.ToPRSeconds != nil {
			toPR += *r.ToPRSeconds
			nToPR++
			if r.Staged {
				s.StagedToPR++
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s.Issues++
		if pr[k] {
			s.WithPR++
		}
		if merged[k] {
			s.Merged++
		}
		if l := lanes[k]; len(l) > 0 {
			b, r := rework(l)
			s.IssuesWithDispatches++
			s.Bounces += b
			s.Redispatches += r
		}
	}
	if nToPR > 0 {
		v := toPR / float64(nToPR)
		s.AvgToPRSeconds = &v
	}
	if s.Merged > 0 && priced {
		v := cost / float64(s.Merged)
		s.CostPerMerged = &v
	}
	if s.IssuesWithDispatches > 0 {
		v := float64(s.Bounces) / float64(s.IssuesWithDispatches)
		s.ReworkPerIssue = &v
	}
	return s
}

var itemKey = regexp.MustCompile(`^(.+)#(\d+)$`)

// summaryAPI is summarize over the items of one factory and repo (both optional).
func (s *server) summaryAPI(r *http.Request) (any, error) {
	all, err := report.ItemRows(s.db, time.Now())
	if err != nil {
		return nil, err
	}
	f, repo := r.URL.Query().Get("factory"), r.URL.Query().Get("repo")
	var rows []report.ItemRow
	for _, u := range all {
		m := itemKey.FindStringSubmatch(u.Item)
		if (f == "" || u.Factory == f) && (repo == "" || (m != nil && m[1] == repo)) {
			rows = append(rows, u)
		}
	}
	var lanes map[string][]string
	if s.local && s.o.Runs != "" {
		// shortcut: manifests name cards by issue number only; two repos with the same issue number share rework counts
		rl, err := s.manifests()
		if err != nil {
			return nil, err
		}
		ds := rl.dispatches
		lanes = map[string][]string{}
		for _, u := range rows {
			if m := itemKey.FindStringSubmatch(u.Item); m != nil {
				n, _ := strconv.ParseInt(m[2], 10, 64)
				for _, d := range ds[n] {
					lanes[u.Item] = append(lanes[u.Item], d.Lane)
				}
			}
		}
	}
	return summarize(rows, lanes), nil
}
