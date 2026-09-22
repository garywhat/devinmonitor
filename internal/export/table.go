package export

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Table is a generic tabular result that can be rendered to CSV, Markdown,
// HTML, or JSON. It backs `export <report_type>` for the non-session reports
// (daily/weekly/monthly/models/projects/agents), which each have their own
// column set rather than the session-oriented writers.
type Table struct {
	Headers []string
	Rows    [][]string
}

// WriteTableCSV renders the table as CSV with a header row.
func WriteTableCSV(w io.Writer, t Table) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(t.Headers); err != nil {
		return err
	}
	for _, r := range t.Rows {
		if err := cw.Write(r); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// WriteTableMarkdown renders the table as a GitHub-flavored Markdown table.
func WriteTableMarkdown(w io.Writer, t Table) error {
	if _, err := fmt.Fprintf(w, "| %s |\n", strings.Join(mdEscapeAll(t.Headers), " | ")); err != nil {
		return err
	}
	sep := make([]string, len(t.Headers))
	for i := range sep {
		sep[i] = "---"
	}
	if _, err := fmt.Fprintf(w, "| %s |\n", strings.Join(sep, " | ")); err != nil {
		return err
	}
	for _, r := range t.Rows {
		if _, err := fmt.Fprintf(w, "| %s |\n", strings.Join(mdEscapeAll(r), " | ")); err != nil {
			return err
		}
	}
	return nil
}

// WriteTableHTML renders the table as a standalone HTML table fragment.
func WriteTableHTML(w io.Writer, t Table) error {
	if _, err := io.WriteString(w, "<table>\n<thead>\n<tr>"); err != nil {
		return err
	}
	for _, h := range t.Headers {
		if _, err := fmt.Fprintf(w, "<th>%s</th>", htmlEscape(h)); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(w, "</tr>\n</thead>\n<tbody>\n"); err != nil {
		return err
	}
	for _, r := range t.Rows {
		if _, err := io.WriteString(w, "<tr>"); err != nil {
			return err
		}
		for _, c := range r {
			if _, err := fmt.Fprintf(w, "<td>%s</td>", htmlEscape(c)); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, "</tr>\n"); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "</tbody>\n</table>\n")
	return err
}

// WriteTableJSON renders the table as a JSON array of objects keyed by the
// header names.
func WriteTableJSON(w io.Writer, t Table) error {
	out := make([]map[string]string, 0, len(t.Rows))
	for _, r := range t.Rows {
		obj := make(map[string]string, len(t.Headers))
		for i, h := range t.Headers {
			if i < len(r) {
				obj[h] = r[i]
			}
		}
		out = append(out, obj)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func mdEscapeAll(cells []string) []string {
	out := make([]string, len(cells))
	for i, c := range cells {
		out[i] = mdEscape(c)
	}
	return out
}
