package normalize

import (
	"encoding/json"
	"strings"
)

// GitHub payloads are wrappers around the raw API objects, written by the reconciler.
type ghPR struct {
	Repo string `json:"repo"`
	PR   struct {
		Number   int64   `json:"number"`
		HTMLURL  string  `json:"html_url"`
		State    string  `json:"state"`
		MergedAt *string `json:"merged_at"`
		// only on the single-PR object the reconciler fetches for factory PRs
		Additions    *int64 `json:"additions"`
		Deletions    *int64 `json:"deletions"`
		ChangedFiles *int64 `json:"changed_files"`
		Head         struct {
			Ref string `json:"ref"`
		} `json:"head"`
	} `json:"pr"`
}

type ghCommits struct {
	Repo    string `json:"repo"`
	Number  int64  `json:"number"`
	Commits []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Author struct {
				Email string `json:"email"`
				Date  string `json:"date"`
			} `json:"author"`
		} `json:"commit"`
		Author *struct {
			Login string `json:"login"`
		} `json:"author"`
	} `json:"commits"`
}

type ghCheck struct {
	Repo     string `json:"repo"`
	PRNumber int64  `json:"pr_number"`
	Check    struct {
		ID          int64   `json:"id"`
		Name        string  `json:"name"`
		HeadSHA     string  `json:"head_sha"`
		Conclusion  *string `json:"conclusion"`
		StartedAt   *string `json:"started_at"`
		CompletedAt *string `json:"completed_at"`
	} `json:"check_run"`
}

type ghReviews struct {
	Repo    string `json:"repo"`
	Number  int64  `json:"number"`
	Reviews []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		State       string  `json:"state"`
		SubmittedAt *string `json:"submitted_at"`
	} `json:"reviews"`
}

type ghIssue struct {
	Repo  string `json:"repo"`
	Issue struct {
		Number   int64   `json:"number"`
		Title    string  `json:"title"`
		State    string  `json:"state"`
		ClosedAt *string `json:"closed_at"`
		ClosedBy *struct {
			Login string `json:"login"`
		} `json:"closed_by"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	} `json:"issue"`
}

// github accumulates GitHub events; for commits, reviews, and issues the latest event wins.
type github struct {
	commits    map[string][]prCommit // repo#pr
	reviews    map[string][]review   // repo#pr
	order      []string              // repo#pr keys of commits and reviews, first-seen order
	checks     map[int64]check
	checkOrder []int64
	issues     map[string]ghIssue // repo#issue
	emails     map[string]string
	logins     map[string]string
}

func (g *github) seen(k string) {
	if g.commits[k] == nil && g.reviews[k] == nil {
		g.order = append(g.order, k)
	}
}

func (g *github) add(e event, prs map[string]*pr, order *[]string) {
	switch e.Type {
	case "pr":
		var v ghPR
		if json.Unmarshal(e.Payload, &v) != nil {
			return
		}
		k := v.Repo + "#" + itoa(v.PR.Number)
		if prs[k] == nil {
			*order = append(*order, k)
		}
		state := v.PR.State
		if v.PR.MergedAt != nil {
			state = "merged"
		}
		p := &pr{Repo: v.Repo, Number: v.PR.Number, URL: v.PR.HTMLURL, Branch: str(v.PR.Head.Ref), State: &state, Merged: v.PR.MergedAt,
			Additions: v.PR.Additions, Deletions: v.PR.Deletions, Files: v.PR.ChangedFiles}
		if old := prs[k]; old != nil && p.Additions == nil { // a list item doesn't erase the size of the full object
			p.Additions, p.Deletions, p.Files = old.Additions, old.Deletions, old.Files
		}
		prs[k] = p
	case "pr.commits":
		var v ghCommits
		if json.Unmarshal(e.Payload, &v) != nil {
			return
		}
		k := v.Repo + "#" + itoa(v.Number)
		g.seen(k)
		cs := []prCommit{}
		for _, cm := range v.Commits {
			email := strings.ToLower(cm.Commit.Author.Email)
			f := g.emails[email]
			if f == "" && cm.Author != nil {
				f = g.logins[strings.ToLower(cm.Author.Login)]
			}
			cs = append(cs, prCommit{Repo: v.Repo, PR: v.Number, SHA: cm.SHA, Email: str(email), Factory: str(f), Committed: str(cm.Commit.Author.Date)})
		}
		g.commits[k] = cs
	case "pr.reviews":
		var v ghReviews
		if json.Unmarshal(e.Payload, &v) != nil {
			return
		}
		k := v.Repo + "#" + itoa(v.Number)
		g.seen(k)
		rs := []review{}
		for _, r := range v.Reviews {
			rs = append(rs, review{Repo: v.Repo, PR: v.Number, Reviewer: r.User.Login, State: r.State, Submitted: r.SubmittedAt})
		}
		g.reviews[k] = rs
	case "issue":
		var v ghIssue
		if json.Unmarshal(e.Payload, &v) != nil {
			return
		}
		g.issues[v.Repo+"#"+itoa(v.Issue.Number)] = v
	case "check_run":
		var v ghCheck
		if json.Unmarshal(e.Payload, &v) != nil {
			return
		}
		if _, seen := g.checks[v.Check.ID]; !seen {
			g.checkOrder = append(g.checkOrder, v.Check.ID)
		}
		g.checks[v.Check.ID] = check{Repo: v.Repo, PR: v.PRNumber, Name: v.Check.Name, HeadSHA: v.Check.HeadSHA,
			Conclusion: v.Check.Conclusion, Started: v.Check.StartedAt, Done: v.Check.CompletedAt}
	}
}
