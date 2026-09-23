package share

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/garywhat/devinmonitor/internal/errscan"
	"github.com/garywhat/devinmonitor/internal/model"
)

// renderHTML is the shared test helper: it renders a report and fails the test
// if the template errors.
func renderHTML(t *testing.T, rep Report) string {
	t.Helper()
	var buf bytes.Buffer
	if err := RenderHTML(&buf, rep); err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	return buf.String()
}

// ---- normal report: sections and rows ----

func TestRenderHTMLHasSectionsAndRows(t *testing.T) {
	out := renderHTML(t, Build(fixtureSessions(), true, time.Now()))

	for _, want := range []string{
		"<html",
		"</html>",
		"<style>",
		"Summary",
		"Usage by model",
		"Usage by project",
		"Error categories",
		"Redaction manifest",
		"scope=\"col\"",
		"scope=\"row\"",
		"<caption>",
		Notice,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered HTML is missing %q", want)
		}
	}

	// The rows themselves: one model, one project label.
	if !strings.Contains(out, "claude-sonnet-4-5") {
		t.Error("rendered HTML is missing the model row")
	}
	if !strings.Contains(out, "secret-project") {
		t.Error("rendered HTML is missing the sanitised project row")
	}
	if !strings.Contains(out, "2") {
		t.Error("rendered HTML is missing aggregate counts")
	}

	// Without --include-errors there is no errors section at all.
	noErrors := renderHTML(t, Build(fixtureSessions(), false, time.Now()))
	if strings.Contains(noErrors, "Error categories") {
		t.Error("includeErrors=false must not render the error-category section")
	}
}

// ---- escaping ----

func TestRenderHTMLEscapesUntrustedNames(t *testing.T) {
	// Both hostile names arrive the way real data does: through Build, from a
	// session whose model and working directory are attacker-influenced.
	ss := []model.Session{{
		ID:             fixtureID1,
		WorkingDir:     `a&b"c`,
		LatestModel:    `<script>alert(1)</script>`,
		AgentMode:      "normal",
		CreditCost:     0.25,
		InputTokens:    10,
		OutputTokens:   5,
		AssistantCount: 1,
	}}

	out := renderHTML(t, Build(ss, false, time.Now()))

	if strings.Contains(out, "<script") {
		t.Fatalf("a model name injected a script tag:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("model name was not escaped as expected")
	}
	if !strings.Contains(out, "a&amp;b&#34;c") {
		t.Errorf("project name was not escaped as expected")
	}
	if strings.Contains(out, `a&b"c`) {
		t.Error("project name survived unescaped")
	}
}

// ---- self-containment ----

func TestRenderHTMLIsSelfContained(t *testing.T) {
	out := renderHTML(t, Build(fixtureSessions(), true, time.Now()))

	for _, banned := range []string{
		"http://",
		"https://",
		"<script",
		"<link",
		"src=",
		"<img",
		"<iframe",
		"@import",
		"url(",
		"<object",
		"<embed",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("self-contained page must not contain %q", banned)
		}
	}

	// Exactly one inline style block, and nothing that loads a resource.
	if n := strings.Count(out, "<style>"); n != 1 {
		t.Errorf("want exactly one inline <style> block, got %d", n)
	}
	if n := strings.Count(out, "</style>"); n != 1 {
		t.Errorf("want exactly one </style>, got %d", n)
	}
}

// ---- the sanitisation invariants, applied to the HTML bytes ----

func TestRenderHTMLKeepsSanitisationInvariants(t *testing.T) {
	out := renderHTML(t, Build(fixtureSessions(), true, time.Now()))

	forbidden := []string{
		fixtureID1,
		fixtureID2,
		fixtureUnix,
		fixtureWin,
		fixtureParent,
		fixtureWinDir,
		"/Users/",
		`C:\Users`,
		fixtureErr,
		"SECRET_RAW_ERROR_TEXT_9f3a",
		"connection refused",
		"10.0.0.7",
		"fix the flaky test",
		"why did",
	}
	for _, f := range forbidden {
		if strings.Contains(out, f) {
			t.Errorf("rendered HTML leaks %q", f)
		}
	}

	// Positive control: the sanitised label is still there, so the assertions
	// above are not passing because nothing rendered at all.
	if !strings.Contains(out, "secret-project") {
		t.Error("rendered HTML should carry the sanitised project label")
	}
}

// ---- empty report ----

func TestRenderHTMLEmptyReport(t *testing.T) {
	out := renderHTML(t, Build(nil, false, time.Now()))

	if !strings.Contains(out, "</html>") {
		t.Error("empty report must still close the document")
	}
	for _, want := range []string{"Usage by model", "Usage by project", "No model usage recorded.", "No project usage recorded."} {
		if !strings.Contains(out, want) {
			t.Errorf("empty report is missing %q", want)
		}
	}

	assertBalanced(t, out)
}

// ---- format validation (no shelling out) ----

func TestCheckFormat(t *testing.T) {
	valid := []string{"json", "html", "JSON", " HTML "}
	for _, f := range valid {
		if !validFormat(f) {
			t.Errorf("validFormat(%q) = false, want true", f)
		}
		if err := checkFormat(f); err != nil {
			t.Errorf("checkFormat(%q) = %v, want nil", f, err)
		}
	}

	for _, f := range []string{"bogus", "", "xml", "htm", "jsonl", "json html"} {
		if validFormat(f) {
			t.Errorf("validFormat(%q) = true, want false", f)
		}
		err := checkFormat(f)
		if err == nil {
			t.Fatalf("checkFormat(%q) = nil, want an error (the command exits non-zero on it)", f)
		}
		if !strings.Contains(err.Error(), f) {
			t.Errorf("checkFormat(%q) error should name the offending value: %v", f, err)
		}
	}

	// The default must be the current behaviour.
	if FormatJSON != "json" {
		t.Errorf("FormatJSON = %q, want json", FormatJSON)
	}
}

// ---- structural validity ----
//
// golang.org/x/net/html is not a dependency of this module (checked go.mod), so
// the page is validated structurally instead of by parsing it with a real HTML
// parser: balanced counts for every element the renderer emits, and a single
// document root.
func TestRenderHTMLIsStructurallyValid(t *testing.T) {
	for _, rep := range []Report{
		Build(fixtureSessions(), true, time.Now()),
		Build(nil, false, time.Now()),
	} {
		out := renderHTML(t, rep)
		assertBalanced(t, out)
	}
}

func assertBalanced(t *testing.T, out string) {
	t.Helper()

	if n := strings.Count(out, "<html"); n != 1 {
		t.Errorf("want exactly one <html>, got %d", n)
	}
	if n := strings.Count(out, "</html>"); n != 1 {
		t.Errorf("want exactly one </html>, got %d", n)
	}
	if strings.Contains(out, "</script") || strings.Contains(out, "<script") {
		t.Error("page must not contain any script")
	}

	pairs := []struct{ open, close string }{
		{"<table", "</table>"},
		{"<thead>", "</thead>"},
		{"<tbody", "</tbody>"},
		{"<caption>", "</caption>"},
		{"<tr>", "</tr>"},
		{"<th ", "</th>"},
		{"<td", "</td>"},
		{"<section>", "</section>"},
		{"<main>", "</main>"},
		{"<footer>", "</footer>"},
	}
	for _, p := range pairs {
		o, c := strings.Count(out, p.open), strings.Count(out, p.close)
		if o != c {
			t.Errorf("unbalanced %s/%s: %d open, %d close", p.open, p.close, o, c)
		}
		if o == 0 {
			t.Errorf("expected at least one %s in the page", p.open)
		}
	}

	// A <table> with no rows would be a broken table; every table here has a
	// header row or a fallback row.
	if n := strings.Count(out, "<table"); n == 0 {
		t.Error("page should render tables")
	}
}

// ---- JSON/HTML delta ----

// uuidish matches a session-ID-shaped token, the one identifier class the HTML
// page could conceivably surface if the aggregation were bypassed.
var uuidish = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func TestRenderHTMLCarriesNoSessionIDShapedToken(t *testing.T) {
	jsonDoc := mustMarshal(t, Build(fixtureSessions(), true, time.Now()))
	htmlDoc := renderHTML(t, Build(fixtureSessions(), true, time.Now()))

	if uuidish.MatchString(htmlDoc) {
		t.Errorf("rendered HTML contains a session-ID-shaped token:\n%s", htmlDoc)
	}
	if uuidish.MatchString(jsonDoc) {
		t.Fatalf("fixture/JSON unexpectedly carries a session-ID-shaped token; the test is not meaningful")
	}

	// Everything the page names must already be named by the JSON report.
	for _, label := range []string{"claude-sonnet-4-5", "secret-project"} {
		if !strings.Contains(jsonDoc, label) {
			t.Errorf("HTML names %q but the JSON report does not", label)
		}
	}

	// And the reverse of the promise: nothing identifying is in the HTML either.
	for _, id := range []string{fixtureID1, fixtureID2} {
		if strings.Contains(htmlDoc, id) {
			t.Errorf("HTML leaks session ID %q", id)
		}
	}
}

// ---- grouping helper ----

func TestGroupDigits(t *testing.T) {
	cases := map[int64]string{
		0:          "0",
		7:          "7",
		999:        "999",
		1000:       "1,000",
		4800:       "4,800",
		1234567:    "1,234,567",
		-1234567:   "-1,234,567",
		1000000000: "1,000,000,000",
	}
	for in, want := range cases {
		if got := groupDigits(in); got != want {
			t.Errorf("groupDigits(%d) = %q, want %q", in, got, want)
		}
	}
}

// ---- typing ----

func TestRenderHTMLNeverRendersErrorFindings(t *testing.T) {
	rep := Build(fixtureSessions(), true, time.Now())
	er, ok := rep.Errors.(*errscan.Report)
	if !ok || er == nil {
		t.Fatal("fixture should produce a typed errors section with includeErrors=true")
	}

	// Simulate a report that still carries findings (Build drops them, but the
	// renderer must not depend on that): the renderer is the last line of
	// defence, so neither the message body nor the session ID may appear.
	er.Findings = []errscan.Finding{{
		Category:  "other",
		SessionID: fixtureID1,
		Model:     "claude-sonnet-4-5",
		Content:   "SECRET_FINDING_CONTENT_4b21",
	}}

	out := renderHTML(t, rep)
	if !strings.Contains(out, "Error categories") {
		t.Error("typed error report should render the error-category section")
	}
	if strings.Contains(out, "SECRET_FINDING_CONTENT_4b21") {
		t.Error("the HTML page rendered a raw error finding")
	}
	if strings.Contains(out, fixtureID1) {
		t.Error("the HTML page rendered a session ID from a finding")
	}
}
