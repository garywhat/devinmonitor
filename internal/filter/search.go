package filter

import (
	"sort"
	"strings"

	"github.com/garywhat/devinmonitor/internal/report"
)

// ProjectSearch filters project rows by a substring query (case-insensitive)
// against the project name. Returns a new slice; the input is not mutated.
func ProjectSearch(rows []report.ProjectRow, query string) []report.ProjectRow {
	if query == "" {
		return rows
	}
	q := strings.ToLower(query)
	var out []report.ProjectRow
	for _, r := range rows {
		if strings.Contains(strings.ToLower(r.Name), q) {
			out = append(out, r)
		}
	}
	return out
}

// MergeProjects merges the named projects into the first one in the list,
// summing their stats. names is a comma-separated list of project names to
// merge together. The merged group keeps the first name as the canonical name.
func MergeProjects(rows []report.ProjectRow, names string) []report.ProjectRow {
	toMerge := splitCSV(names)
	if len(toMerge) < 2 {
		return rows
	}
	mergeSet := map[string]bool{}
	for _, n := range toMerge {
		mergeSet[n] = true
	}
	canonical := toMerge[0]

	var merged *report.ProjectRow
	var rest []report.ProjectRow
	for _, r := range rows {
		if mergeSet[r.Name] {
			if merged == nil {
				rr := r
				rr.Name = canonical
				merged = &rr
			} else {
				merged.Sessions += r.Sessions
				merged.Requests += r.Requests
				merged.InputTok += r.InputTok
				merged.OutputTok += r.OutputTok
				merged.CacheRead += r.CacheRead
				merged.CacheWrite += r.CacheWrite
				merged.Cost += r.Cost
				for _, m := range r.Models {
					if !containsString(merged.Models, m) {
						merged.Models = append(merged.Models, m)
					}
				}
			}
		} else {
			rest = append(rest, r)
		}
	}
	if merged == nil {
		return rows
	}
	out := append(rest, *merged)
	sort.Slice(out, func(i, j int) bool { return out[i].Requests > out[j].Requests })
	return out
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
