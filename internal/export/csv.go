package export

import (
	"encoding/csv"
	"io"
	"strconv"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
)

// csvHeaders is the column set for session CSV exports.
var csvHeaders = []string{
	"id", "title", "model", "project", "cost", "tokens",
	"duration", "created_at",
}

// WriteCSV writes sessions as CSV. Columns: id, title, model, project, cost,
// tokens, duration, created_at. The cost column uses the authoritative
// credit/ACU value when available, otherwise the pricing estimate.
func WriteCSV(w io.Writer, ss []model.Session) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeaders); err != nil {
		return err
	}
	for _, s := range ss {
		cost := sessionCostCSV(&s)
		tokens := s.InputTokens + s.OutputTokens + s.CacheRead + s.CacheWrite
		dur := s.LastActivityAt.Sub(s.CreatedAt).Seconds()
		row := []string{
			s.ID,
			s.Title,
			s.Model,
			baseProject(s.WorkingDir),
			strconv.FormatFloat(cost, 'f', 4, 64),
			strconv.FormatInt(tokens, 10),
			strconv.FormatFloat(dur, 'f', 1, 64),
			s.CreatedAt.Format(time.RFC3339),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func sessionCostCSV(s *model.Session) float64 {
	if s.CreditCost > 0 || s.ACUCost > 0 {
		return s.CreditCost + s.ACUCost
	}
	p := model.LookupPricing(s.Model)
	return model.EstimateCost(p, s.InputTokens, s.OutputTokens, s.CacheRead, s.CacheWrite)
}
