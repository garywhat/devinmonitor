package share

import (
	"fmt"
	"html/template"
	"io"
	"strconv"
	"strings"

	"github.com/garywhat/devinmonitor/internal/errscan"
)

// Output formats accepted by `share --format`.
const (
	FormatJSON = "json"
	FormatHTML = "html"
)

// normalizeFormat lowercases and trims a user-supplied format so `--format
// HTML` behaves like `--format html` without widening what is accepted.
func normalizeFormat(f string) string {
	return strings.ToLower(strings.TrimSpace(f))
}

// validFormat reports whether f names an output format this package can write.
func validFormat(f string) bool {
	switch normalizeFormat(f) {
	case FormatJSON, FormatHTML:
		return true
	}
	return false
}

// checkFormat validates --format. Run prints the returned error to stderr and
// exits non-zero; factoring it out keeps the usage-error path testable without
// spawning a process.
func checkFormat(f string) error {
	if validFormat(f) {
		return nil
	}
	return fmt.Errorf("unknown --format %q: expected %q or %q", f, FormatJSON, FormatHTML)
}

// htmlData is the template payload. The report's Errors field is an
// interface{}; it is carried separately as a concrete *errscan.Report so the
// template never has to render an untyped value.
type htmlData struct {
	Report
	ErrorReport *errscan.Report
	HasErrors   bool
}

// RenderHTML writes the self-contained HTML rendering of a sanitised report.
//
// The page is deliberately script-free and reference-free: all styling is
// inline, and there is no stylesheet, font, image or script to fetch. Every
// interpolated value goes through html/template, so a hostile model or project
// name cannot inject markup. The rendering is fed by Report alone — it adds no
// data of its own — so the same four invariants the JSON report satisfies
// (no absolute paths, no session IDs, no raw error text, no message content)
// hold here too. Error findings are never rendered even if a caller hands over
// a report that still carries them.
func RenderHTML(w io.Writer, rep Report) error {
	d := htmlData{Report: rep}
	if er, ok := rep.Errors.(*errscan.Report); ok && er != nil {
		d.ErrorReport = er
		d.HasErrors = true
	}
	return htmlTemplate.Execute(w, d)
}

// htmlTemplate renders the page. Funcs only format numbers; none of them
// produces markup, so escaping stays html/template's job.
var htmlTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"money":  func(v float64) string { return strconv.FormatFloat(v, 'f', 4, 64) },
	"pct":    func(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) + "%" },
	"tokens": groupDigits,
}).Parse(htmlPage))

// groupDigits renders an integer with thousands separators so large token
// counts stay readable without any client-side formatting.
func groupDigits(v int64) string {
	s := strconv.FormatInt(v, 10)
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	if len(s) <= 3 {
		return sign + s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return sign + b.String()
}

// htmlPage is the whole document. It is intentionally small: semantic tables,
// a handful of CSS rules, and no scripting. Section headings are stable so
// tests can assert them.
const htmlPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>devinmonitor usage report</title>
<style>
body { margin: 0 auto; padding: 1.5rem 1rem 3rem; max-width: 56rem; line-height: 1.5; color: #16181d; background: #fff; font-family: system-ui, -apple-system, "Segoe UI", sans-serif; }
h1 { margin: 0 0 1rem; font-size: 1.5rem; }
h2 { margin: 2rem 0 .5rem; padding-bottom: .25rem; border-bottom: 1px solid #d0d3d9; font-size: 1.15rem; }
p.notice { margin: 1rem 0; padding: .75rem 1rem; border-left: 4px solid #4a6fa5; background: #f4f6f8; }
table { margin: .5rem 0 1rem; border-collapse: collapse; width: 100%; }
caption { padding-bottom: .35rem; color: #4a4f57; text-align: left; font-size: .85rem; }
th, td { padding: .4rem .6rem; border: 1px solid #c9ccd2; text-align: left; vertical-align: top; }
th { background: #eef1f4; font-weight: 600; }
th.num, td.num { text-align: right; font-variant-numeric: tabular-nums; }
footer { margin-top: 2.5rem; padding-top: .75rem; border-top: 1px solid #d0d3d9; color: #4a4f57; font-size: .85rem; }
</style>
</head>
<body>
<h1>devinmonitor usage report</h1>
<p class="notice">{{.Notice}}</p>

<main>
<section>
<h2>Summary</h2>
<table>
<caption>Aggregate totals for the sessions analysed on this machine.</caption>
<tbody>
<tr><th scope="row">Sessions</th><td class="num">{{.Usage.TotalSessions}}</td></tr>
<tr><th scope="row">Requests</th><td class="num">{{.Usage.TotalRequests}}</td></tr>
<tr><th scope="row">Tokens</th><td class="num">{{tokens .Usage.TotalTokens}}</td></tr>
<tr><th scope="row">Cost</th><td class="num">{{money .Usage.TotalCost}}</td></tr>
<tr><th scope="row">Cost provenance</th><td>{{.Usage.CostProvenance}}</td></tr>
<tr><th scope="row">Generated at</th><td>{{.GeneratedAt}}</td></tr>
<tr><th scope="row">Schema version</th><td class="num">{{.SchemaVersion}}</td></tr>
</tbody>
</table>
</section>

<section>
<h2>Usage by model</h2>
<table>
<caption>One row per model. Token and cost figures are sums over the analysed sessions.</caption>
<thead>
<tr>
<th scope="col">Model</th>
<th scope="col" class="num">Requests</th>
<th scope="col" class="num">Input tokens</th>
<th scope="col" class="num">Output tokens</th>
<th scope="col" class="num">Cost</th>
</tr>
</thead>
<tbody>
{{range .Usage.ByModel}}<tr>
<td>{{.Model}}</td>
<td class="num">{{.Requests}}</td>
<td class="num">{{tokens .InputTokens}}</td>
<td class="num">{{tokens .OutputTokens}}</td>
<td class="num">{{money .Cost}}</td>
</tr>
{{else}}<tr><td colspan="5">No model usage recorded.</td></tr>
{{end}}</tbody>
</table>
</section>

<section>
<h2>Usage by project</h2>
<table>
<caption>Projects are identified by the last segment of each working directory only.</caption>
<thead>
<tr>
<th scope="col">Project</th>
<th scope="col" class="num">Sessions</th>
<th scope="col" class="num">Cost</th>
</tr>
</thead>
<tbody>
{{range .Usage.ByProject}}<tr>
<td>{{.Project}}</td>
<td class="num">{{.Sessions}}</td>
<td class="num">{{money .Cost}}</td>
</tr>
{{else}}<tr><td colspan="3">No project usage recorded.</td></tr>
{{end}}</tbody>
</table>
</section>

{{if .HasErrors}}<section>
<h2>Error categories</h2>
<table>
<caption>Per-category counts only: error messages and findings are never included. Error rate {{pct .ErrorReport.ErrorRate}} of {{.ErrorReport.AssistantMessages}} assistant messages.</caption>
<thead>
<tr>
<th scope="col">Category</th>
<th scope="col" class="num">Count</th>
<th scope="col" class="num">Share</th>
</tr>
</thead>
<tbody>
{{range .ErrorReport.ByCategory}}<tr>
<td>{{.Category}}</td>
<td class="num">{{.Count}}</td>
<td class="num">{{pct .Percent}}</td>
</tr>
{{else}}<tr><td colspan="3">No categorised errors ({{.ErrorReport.Total}} total).</td></tr>
{{end}}</tbody>
</table>
</section>
{{end}}

<section>
<h2>Redaction manifest</h2>
<table>
<caption>Every sanitisation rule applied to this report, so a recipient can audit what was removed before the data left the machine.</caption>
<thead>
<tr>
<th scope="col">Field</th>
<th scope="col">Action</th>
<th scope="col">Reason</th>
</tr>
</thead>
<tbody>
{{range .Redactions}}<tr>
<td>{{.Field}}</td>
<td>{{.Action}}</td>
<td>{{.Reason}}</td>
</tr>
{{else}}<tr><td colspan="3">No redaction rules recorded.</td></tr>
{{end}}</tbody>
</table>
</section>
</main>

<footer>
<p>Produced locally by devinmonitor. This page is a single self-contained file: it loads no stylesheet, font, image or script, and it makes no network requests.</p>
</footer>
</body>
</html>
`
