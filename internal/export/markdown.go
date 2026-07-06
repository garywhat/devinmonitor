package export

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
)

// mdHeaders is the column set for the markdown session table.
var mdHeaders = []string{"ID", "Title", "Model", "Project", "Cost", "Tokens", "Duration", "Created"}

// WriteMarkdown writes sessions as a paste-friendly GitHub-flavored markdown
// table. Numeric columns are right-aligned via the separator row.
func WriteMarkdown(w io.Writer, ss []model.Session) error {
	var b strings.Builder
	b.WriteString("| " + strings.Join(mdHeaders, " | ") + " |\n")
	// Right-align numeric columns (Cost, Tokens, Duration): indices 4,5,6.
	sep := make([]string, len(mdHeaders))
	for i := range sep {
		sep[i] = "---"
	}
	sep[4], sep[5], sep[6] = "---:", "---:", "---:"
	b.WriteString("| " + strings.Join(sep, " | ") + " |\n")

	for _, s := range ss {
		cost := sessionCostCSV(&s)
		tokens := s.InputTokens + s.OutputTokens + s.CacheRead + s.CacheWrite
		dur := s.LastActivityAt.Sub(s.CreatedAt)
		row := []string{
			"`" + s.ID + "`",
			mdEscape(s.Title),
			mdEscape(s.Model),
			mdEscape(baseProject(s.WorkingDir)),
			fmt.Sprintf("$%.2f", cost),
			fmt.Sprintf("%d", tokens),
			fmt.Sprintf("%s", dur.Round(time.Second)),
			s.CreatedAt.Format("2006-01-02"),
		}
		b.WriteString("| " + strings.Join(row, " | ") + " |\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func mdEscape(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
