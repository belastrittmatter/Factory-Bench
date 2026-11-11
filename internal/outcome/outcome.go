// Package outcome derives a work item's outcome; normalize stores it and report recomputes it per factory.
package outcome

// FromPRStates is the outcome from the states of the linked PRs: merged > rejected (all closed) > open.
func FromPRStates(prStates map[string]bool) string {
	switch {
	case prStates["merged"]:
		return "merged"
	case prStates["closed"] && !prStates["open"]:
		return "rejected"
	}
	return "open"
}
