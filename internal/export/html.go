package export

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
)

// htmlHeaders is the column set for the HTML session table.
var htmlHeaders = []string{"ID", "Title", "Model", "Project", "Cost", "Tokens", "Duration", "Created"}

// WriteHTML writes a self-contained interactive HTML snapshot with a styled
// session table and lightweight client-side filtering. No external assets.
func WriteHTML(w io.Writer, ss []model.Session) error {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>DevinMonitor Snapshot</title>
<style>
  :root { color-scheme: dark; }
  body { font: 14px/1.5 -apple-system,Segoe UI,Roboto,sans-serif; background:#1e1e2e; color:#cdd6f4; margin:0; padding:24px; }
  h1 { color:#cba6f7; margin-top:0; }
  .meta { color:#9399b2; margin-bottom:16px; }
  input { padding:6px 10px; border:1px solid #45475a; border-radius:6px; background:#313244; color:#cdd6f4; width:240px; margin-bottom:12px; }
  table { border-collapse:collapse; width:100%; }
  th, td { padding:8px 10px; text-align:left; border-bottom:1px solid #45475a; }
  th { color:#cba6f7; cursor:pointer; user-select:none; position:sticky; top:0; background:#181825; }
  tr:hover td { background:#313244; }
  .num { text-align:right; font-variant-numeric:tabular-nums; }
  .id { font-family:ui-monospace,monospace; font-size:12px; }
</style>
</head>
<body>
<h1>DevinMonitor Snapshot</h1>
<div class="meta">Generated `)
	b.WriteString(time.Now().Format(time.RFC3339))
	b.WriteString(" &middot; ")
	fmt.Fprintf(&b, "%d sessions", len(ss))
	b.WriteString(`</div>
<input type="text" placeholder="Filter..." id="f" oninput="filter()">
<table id="t"><thead><tr>`)
	for _, h := range htmlHeaders {
		fmt.Fprintf(&b, "<th>%s</th>", h)
	}
	b.WriteString("</tr></thead><tbody>")
	for _, s := range ss {
		cost := sessionCostCSV(&s)
		tokens := s.InputTokens + s.OutputTokens + s.CacheRead + s.CacheWrite
		dur := s.LastActivityAt.Sub(s.CreatedAt).Round(time.Second)
		b.WriteString("<tr>")
		fmt.Fprintf(&b, `<td><span class="id">%s</span></td>`, htmlEscape(s.ID))
		fmt.Fprintf(&b, `<td>%s</td>`, htmlEscape(s.Title))
		fmt.Fprintf(&b, `<td>%s</td>`, htmlEscape(s.Model))
		fmt.Fprintf(&b, `<td>%s</td>`, htmlEscape(baseProject(s.WorkingDir)))
		fmt.Fprintf(&b, `<td class="num">$%.2f</td>`, cost)
		fmt.Fprintf(&b, `<td class="num">%d</td>`, tokens)
		fmt.Fprintf(&b, `<td class="num">%s</td>`, htmlEscape(dur.String()))
		fmt.Fprintf(&b, `<td>%s</td>`, htmlEscape(s.CreatedAt.Format("2006-01-02")))
		b.WriteString("</tr>")
	}
	b.WriteString(`</tbody></table>
<script>
function filter(){
  var q=document.getElementById('f').value.toLowerCase();
  var rows=document.querySelectorAll('#t tbody tr');
  rows.forEach(function(r){
    r.style.display = r.textContent.toLowerCase().indexOf(q)>=0 ? '' : 'none';
  });
}
</script>
</body>
</html>
`)
	_, err := io.WriteString(w, b.String())
	return err
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}
