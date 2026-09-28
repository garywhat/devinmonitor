package status

// CostBasis names the METER a cost figure came from — which is a different
// question from Confidence, which asks how trustworthy it is.
//
// Devin bills in ACU (action complexity + VM time). ACU has no definitional
// relationship to tokens: there is no exchange rate, no formula and no
// conversion, because ACU measures agent work and VM time while a token is a
// unit of text. Our reports nevertheless put a dollar figure derived from
// Devin's ACU meter next to one we computed as tokens × our own price table,
// and a reader naturally compares them ("this model cost more than that one").
// CostProvenance cannot stop that: both numbers are honestly "estimated" or
// "official", and neither label says they are different quantities.
//
// So the basis travels WITH the figure. It is deliberately orthogonal to
// Confidence:
//
//	costBasis: "acu"            costProvenance: "official"
//	costBasis: "token_estimate" costProvenance: "estimated"
//
// and a reader who sees one number can tell whether the next number is even the
// same kind of thing.
//
// A cost figure is never left unlabelled on the wire: an empty or unrecognised
// basis degrades to CostBasisUnavailable rather than to "".
type CostBasis string

const (
	// CostBasisACU: the figure is Devin's own ACU/credit accounting
	// (sessions.metadata.total_acu_cost / total_credit_cost). This is the only
	// basis that reflects what Devin actually bills.
	CostBasisACU CostBasis = "acu"
	// CostBasisTokenEstimate: the figure was computed locally as token counts ×
	// the model's price in our pricing table. It is a token-basis number even
	// when it resolves to 0 because the model is free or has no price: the
	// arithmetic is ours, and Devin never billed it in ACU.
	CostBasisTokenEstimate CostBasis = "token_estimate"
	// CostBasisMixed: the figure adds ACU-billed and token-estimated costs
	// together. The total is in one currency but not in one unit, so it must be
	// presented with a warning, not as a silently comparable number.
	CostBasisMixed CostBasis = "mixed"
	// CostBasisUnavailable: no basis could be established (no cost signal and
	// no token counts to derive one from).
	CostBasisUnavailable CostBasis = "unavailable"
)

// NormalizeCostBasis maps anything that is not one of the four known values
// onto CostBasisUnavailable, so a caller that forgets to classify a figure
// emits "unavailable" (which is true — nobody established a basis) instead of
// an empty string that would violate the schema's enum.
func NormalizeCostBasis(b CostBasis) CostBasis {
	switch b {
	case CostBasisACU, CostBasisTokenEstimate, CostBasisMixed, CostBasisUnavailable:
		return b
	default:
		return CostBasisUnavailable
	}
}

// CombineCostBasis reduces the bases of the items behind one figure to the basis
// of that figure.
//
// This is where "mixed" is born: a month total that adds three ACU-billed
// sessions to two token-estimated ones has no single unit, and saying so is the
// whole point of the field. An item that is itself mixed contaminates the
// total, so it counts as both bases present.
func CombineCostBasis(bases ...CostBasis) CostBasis {
	var acu, tokens bool
	for _, b := range bases {
		switch NormalizeCostBasis(b) {
		case CostBasisACU:
			acu = true
		case CostBasisTokenEstimate:
			tokens = true
		case CostBasisMixed:
			acu, tokens = true, true
		}
	}
	switch {
	case acu && tokens:
		return CostBasisMixed
	case acu:
		return CostBasisACU
	case tokens:
		return CostBasisTokenEstimate
	default:
		return CostBasisUnavailable
	}
}

// IsMixed reports whether acu-billed and token-estimated figures are added
// together in this basis. Renderers must warn when this is true.
func (b CostBasis) IsMixed() bool { return NormalizeCostBasis(b) == CostBasisMixed }

// Tag renders the basis as the short bracket a printed figure carries, e.g.
// "$1.23 [acu]". Every human cost figure gets one: a number whose unit is not
// on the same line as the number is a number a reader will compare wrongly.
func (b CostBasis) Tag() string {
	switch NormalizeCostBasis(b) {
	case CostBasisACU:
		return "[acu]"
	case CostBasisTokenEstimate:
		return "[token est]"
	case CostBasisMixed:
		return "[mixed units]"
	default:
		return "[no basis]"
	}
}

// Legend is the sentence that makes the tag readable: what produced the figure,
// and — always — that ACU and tokens cannot be converted into one another.
//
// It is emitted next to the figures rather than replaced by the tag alone,
// because "[token est]" next to "$1.23" does not by itself tell a reader that
// the [acu] row above is a different quantity. The last clause is the part that
// stops the comparison, so every branch carries it.
func (b CostBasis) Legend() string {
	switch NormalizeCostBasis(b) {
	case CostBasisACU:
		return "Cost basis: acu — Devin's own ACU meter (action complexity + VM time). " +
			"ACU and tokens are different units with no conversion between them: " +
			"do not compare an acu figure with a token estimate."
	case CostBasisTokenEstimate:
		return "Cost basis: token est — our token counts × model prices, not Devin's meter. " +
			"Devin bills in ACU (action complexity + VM time), a different unit with no conversion " +
			"to tokens: do not compare a token estimate with an acu figure."
	case CostBasisMixed:
		return "Cost basis: MIXED UNITS — this figure adds acu-billed and token-estimated costs together. " +
			"ACU and tokens are different units with no conversion between them: " +
			"compare like with like, never one against the other."
	default:
		return "Cost basis: no basis — no cost or token data was recorded for this figure, " +
			"so it carries no unit at all."
	}
}
