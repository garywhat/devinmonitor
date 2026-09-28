package export

import (
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/status"
)

// SessionCostBasis classifies the meter behind the cost figure that
// report.SessionCost produces for a session.
//
// The rule is deliberately the SAME split SessionCost itself makes, so the label
// can never contradict the number it labels:
//
//	CreditCost/ACUCost > 0                -> acu            (Devin's own meter)
//	otherwise, with token counts to price -> token_estimate (our arithmetic)
//	otherwise                             -> unavailable    (nothing to derive)
//
// Two boundary cases are worth stating, because both look like they deserve a
// fifth value and neither does:
//
//   - A session on a free plan, or one whose model has no price in our table,
//     still has Devin's token counts. Its dollar figure is 0 — but 0 came out
//     of OUR token × price arithmetic, not out of Devin's ACU meter, so the
//     basis is token_estimate. Whether that zero is trustworthy is the
//     provenance dimension's job (costProvenance: "unknown"/"estimated"); the
//     basis dimension only answers "which meter?", and collapsing this case
//     into either "acu" or "unavailable" would recreate the exact confusion
//     this field exists to prevent.
//   - A session with Devin's own token counts (metadata.response_dimensions)
//     but no ACU cost is the normal free-tier shape. Those official counts are
//     NOT an ACU figure: the basis stays token_estimate.
func SessionCostBasis(s *model.Session) status.CostBasis {
	if s == nil {
		return status.CostBasisUnavailable
	}
	if s.CreditCost > 0 || s.ACUCost > 0 {
		return status.CostBasisACU
	}
	if s.InputTokens+s.OutputTokens+s.CacheRead+s.CacheWrite > 0 {
		return status.CostBasisTokenEstimate
	}
	return status.CostBasisUnavailable
}

// CostBasisOfSessions is the basis of a figure that sums every session given.
//
// It is computed from the sessions rather than from the summed dollars: two
// sessions that each cost $0 can still be one ACU-billed session and one
// token-estimated session, and the total is then mixed even though the money
// alone cannot show it.
func CostBasisOfSessions(ss []model.Session) status.CostBasis {
	bases := make([]status.CostBasis, 0, len(ss))
	for i := range ss {
		bases = append(bases, SessionCostBasis(&ss[i]))
	}
	return status.CombineCostBasis(bases...)
}

// CostBasisOfSessionsSince is CostBasisOfSessions restricted to sessions whose
// last activity is strictly after `since`, matching how the daily/weekly/monthly
// cost figures are bucketed elsewhere in the tool.
func CostBasisOfSessionsSince(ss []model.Session, since time.Time) status.CostBasis {
	// Kept as a separate function rather than a filter helper so the two
	// callers (the status snapshot and the snapshot panel) cannot drift.
	bases := make([]status.CostBasis, 0, len(ss))
	for i := range ss {
		if !ss[i].LastActivityAt.After(since) {
			continue
		}
		bases = append(bases, SessionCostBasis(&ss[i]))
	}
	return status.CombineCostBasis(bases...)
}
