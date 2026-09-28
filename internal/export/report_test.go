// Package export's first tests. Before this file the package behind `export`,
// `report`, `backup` and `status` had none at all, which is how `report --days 1`
// managed to print "last 1 days" over a breakdown listing every date in the
// database.
//
// The window tests are the regression guard for that: the text breakdown, the
// SVG chart and the totals above them must all describe one populate.
package export

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
)

// ---- fixtures ----

// sessionAgo builds a session created `days` days before now, carrying one
// assistant request with metrics so it lands in a daily bucket. The message
// timestamp equals the session timestamp, which is the normal shape.
func sessionAgo(id string, now time.Time, days int, cost float64, in, out, cacheRead int64) model.Session {
	ts := now.AddDate(0, 0, -days)
	return model.Session{
		ID:             id,
		Title:          "session " + id,
		Model:          "claude-sonnet-4-5",
		AgentMode:      "normal",
		CreatedAt:      ts,
		LastActivityAt: ts.Add(time.Hour),
		CreditCost:     cost,
		InputTokens:    in,
		OutputTokens:   out,
		CacheRead:      cacheRead,
		AssistantCount: 1,
		Messages: []model.Message{
			{NodeID: 1, Role: "assistant", CreatedAt: ts, GenerationModel: "claude-sonnet-4-5",
				Metrics: &model.Metrics{InputTokens: in, OutputTokens: out, CacheReadTokens: cacheRead}},
		},
	}
}

// spread is a fixture with sessions inside and outside every window the tests
// exercise: today, 2 days ago, 10 days ago, 40 days ago.
func spread(now time.Time) []model.Session {
	return []model.Session{
		sessionAgo("today", now, 0, 1.00, 100, 10, 5),
		sessionAgo("two-days", now, 2, 2.00, 200, 20, 10),
		sessionAgo("ten-days", now, 10, 3.00, 300, 30, 15),
		sessionAgo("forty-days", now, 40, 4.00, 400, 40, 20),
	}
}

var reportLineRe = regexp.MustCompile(`(?m)^  \d{4}-\d{2}-\d{2} `)

func textBucketCount(t *testing.T, ss []model.Session, days int) int {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteReport(&buf, ss, days); err != nil {
		t.Fatalf("WriteReport(days=%d): %v", days, err)
	}
	return len(reportLineRe.FindAllString(buf.String(), -1))
}

func svgBarCount(t *testing.T, ss []model.Session, days int) int {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteReportSVG(&buf, ss, days); err != nil {
		t.Fatalf("WriteReportSVG(days=%d): %v", days, err)
	}
	return strings.Count(buf.String(), `<rect x=`)
}

// ---- item 5: the breakdown must honour the window ----

func TestBuildReportSummaryDailyStaysInsideTheWindow(t *testing.T) {
	now := time.Now()
	ss := spread(now)

	cases := []struct {
		days         int
		wantSessions int
	}{
		{1, 1},  // today only
		{7, 2},  // today + two days ago
		{30, 3}, // + ten days ago
		{0, 4},  // all time
	}
	var breakdowns []string
	for _, c := range cases {
		sum := BuildReportSummary(ss, c.days)
		if sum.Sessions != c.wantSessions {
			t.Errorf("days=%d: Sessions = %d, want %d", c.days, sum.Sessions, c.wantSessions)
		}
		if c.days > 0 {
			start := model.DayStart(now.AddDate(0, 0, -c.days))
			for _, row := range sum.Daily {
				label, err := time.Parse("2006-01-02", row.Label)
				if err != nil {
					t.Fatalf("days=%d: unparseable bucket label %q", c.days, row.Label)
				}
				if label.Before(start) {
					t.Errorf("days=%d: breakdown contains %s, which is before the window start %s",
						c.days, row.Label, start.Format("2006-01-02"))
				}
			}
			if got, want := len(sum.Daily), c.wantSessions; got != want {
				t.Errorf("days=%d: %d daily buckets, want %d (one per in-window session)",
					c.days, got, want)
			}
		} else if sum.Window != "all time" || !sum.From.IsZero() {
			t.Errorf("days=0: Window = %q, From = %v; want \"all time\" and a zero From",
				sum.Window, sum.From)
		}
		// The breakdown, and only the breakdown, is what these tests compare:
		// the totals legitimately include the sessions' aggregate counters.
		var b strings.Builder
		for _, row := range sum.Daily {
			b.WriteString(row.Label + " ")
		}
		breakdowns = append(breakdowns, b.String())
	}

	// The bug this pins: --days 1/7/30 produced byte-identical breakdowns
	// because every one of them was built from the unfiltered session slice.
	for i := 0; i < len(breakdowns)-1; i++ {
		if breakdowns[i] == breakdowns[i+1] {
			t.Errorf("breakdowns for successive windows are identical (%q); the window is being ignored",
				breakdowns[i])
		}
	}
}

func TestReportTextAndSVGDescribeTheSameBuckets(t *testing.T) {
	now := time.Now()
	ss := spread(now)

	for _, days := range []int{1, 7, 30, 0} {
		text, bars := textBucketCount(t, ss, days), svgBarCount(t, ss, days)
		if text != bars {
			t.Errorf("days=%d: text prints %d daily rows but the SVG draws %d bars; "+
				"the two halves of one report must not disagree", days, text, bars)
		}
		if days > 0 && text != 0 && text > days+1 {
			t.Errorf("days=%d: %d daily rows cannot fit a %d-day window", days, text, days)
		}
	}
}

func TestWindowDailyDropsEarlierLabels(t *testing.T) {
	from := time.Date(2026, 3, 10, 13, 45, 0, 0, time.UTC)
	rows := []report.TimeRow{
		{Label: "2026-03-08", Requests: 1},
		{Label: "2026-03-09", Requests: 2}, // the day before the window start
		{Label: "2026-03-10", Requests: 3},
		{Label: "2026-03-12", Requests: 4},
		{Label: "not-a-date", Requests: 5}, // kept: an unparseable label is not proof of age
	}
	got := windowDaily(rows, from)
	var labels []string
	for _, r := range got {
		labels = append(labels, r.Label)
	}
	want := []string{"2026-03-10", "2026-03-12", "not-a-date"}
	if strings.Join(labels, ",") != strings.Join(want, ",") {
		t.Errorf("windowDaily kept %v, want %v", labels, want)
	}
	if out := windowDaily(rows, time.Time{}); len(out) != len(rows) {
		t.Errorf("a zero window start must keep everything, got %d of %d", len(out), len(rows))
	}
}

func TestReportEmptyAndNilInput(t *testing.T) {
	for _, ss := range [][]model.Session{nil, {}} {
		sum := BuildReportSummary(ss, 7)
		if sum.Sessions != 0 || sum.Requests != 0 || len(sum.Daily) != 0 {
			t.Errorf("empty input: %+v, want an empty summary", sum)
		}
		var text, svg bytes.Buffer
		if err := WriteReport(&text, ss, 7); err != nil {
			t.Fatalf("WriteReport(empty): %v", err)
		}
		if err := WriteReportSVG(&svg, ss, 7); err != nil {
			t.Fatalf("WriteReportSVG(empty): %v", err)
		}
		if !strings.Contains(text.String(), "Sessions:  0") {
			t.Errorf("empty report missing zero session count:\n%s", text.String())
		}
		if strings.Contains(svg.String(), "<rect x=") {
			t.Errorf("empty report drew bars:\n%s", svg.String())
		}
	}
}

func TestWriteReportStatesWindowAndTotals(t *testing.T) {
	now := time.Now()
	ss := []model.Session{sessionAgo("today", now, 0, 2.5, 1000, 100, 50)}
	var buf bytes.Buffer
	if err := WriteReport(&buf, ss, 7); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"Window:    last 7 days", "Sessions:  1", "Requests:  1", "Cost:      $2.50"} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, now.Format("2006-01-02")) {
		t.Errorf("report does not name the window end date %s:\n%s", now.Format("2006-01-02"), out)
	}
}

// ---- the compaction split (the vendor cross-check reads these) ----

func TestBuildDocumentExposesCompactionAndBillableTotals(t *testing.T) {
	s := model.Session{
		ID: "s1", Model: "claude-sonnet-4-5", CreditCost: 1,
		InputTokens: 1000, OutputTokens: 200, CacheRead: 300, CacheWrite: 400,
		Compaction: model.CompactionUsage{InputTokens: 100, OutputTokens: 50, CacheRead: 25, CacheWrite: 0},
	}
	doc := BuildDocument([]model.Session{s}, false)
	if len(doc.Sessions) != 1 {
		t.Fatalf("got %d exported sessions, want 1", len(doc.Sessions))
	}
	got := doc.Sessions[0]

	if got.TotalTokens != 1900 {
		t.Errorf("total_tokens = %d, want 1900 (input+output+cacheRead+cacheWrite)", got.TotalTokens)
	}
	if got.Compaction.InputTokens != 100 || got.Compaction.OutputTokens != 50 ||
		got.Compaction.CacheRead != 25 || got.Compaction.CacheWrite != 0 {
		t.Errorf("compaction = %+v, want the session's compaction counters", got.Compaction)
	}
	if got.BillableTokens != 1725 {
		t.Errorf("billable_tokens = %d, want 1725 (total minus compaction)", got.BillableTokens)
	}
	if got.BillableTokens != got.TotalTokens-s.Compaction.Total() {
		t.Error("billable_tokens must be exactly total_tokens minus the compaction share")
	}

	// The field names are part of the contract scripts/verify-against-vendor.sh
	// reads, and one naming convention (snake_case) is the reason they are not
	// camelCase like the surrounding Go field names.
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"total_tokens", "billable_tokens", "compaction"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("exported session is missing %q: %s", key, raw)
		}
	}
	for _, key := range []string{"totalTokens", "billableTokens"} {
		if _, ok := keys[key]; ok {
			t.Errorf("exported session still carries camelCase %q: %s", key, raw)
		}
	}
	var comp map[string]json.RawMessage
	if err := json.Unmarshal(keys["compaction"], &comp); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"input_tokens", "output_tokens", "cache_read", "cache_write"} {
		if _, ok := comp[key]; !ok {
			t.Errorf("compaction object is missing %q: %s", key, keys["compaction"])
		}
	}
	if _, ok := comp["inputTokens"]; ok {
		t.Errorf("compaction object still carries camelCase keys: %s", keys["compaction"])
	}
}

// ---- format selection and the remaining basic coverage ----

func TestExportFormatWriters(t *testing.T) {
	now := time.Now()
	ss := []model.Session{sessionAgo("sess-abc", now, 0, 1.5, 100, 10, 5)}

	writers := map[string]func(*bytes.Buffer) error{
		"csv":      func(b *bytes.Buffer) error { return WriteCSV(b, ss) },
		"markdown": func(b *bytes.Buffer) error { return WriteMarkdown(b, ss) },
		"html":     func(b *bytes.Buffer) error { return WriteHTML(b, ss) },
		"json": func(b *bytes.Buffer) error {
			enc := json.NewEncoder(b)
			return enc.Encode(BuildDocument(ss, true))
		},
	}
	for name, write := range writers {
		var buf bytes.Buffer
		if err := write(&buf); err != nil {
			t.Fatalf("%s writer: %v", name, err)
		}
		if buf.Len() == 0 {
			t.Errorf("%s writer produced nothing", name)
		}
		if !strings.Contains(buf.String(), "sess-abc") {
			t.Errorf("%s output does not mention the session id:\n%s", name, buf.String())
		}
	}

	table, ok := BuildReportTable("models", ss)
	if !ok {
		t.Fatal("BuildReportTable(models) reported an unknown type")
	}
	tableWriters := map[string]func(*bytes.Buffer, Table) error{
		"csv":      func(b *bytes.Buffer, t Table) error { return WriteTableCSV(b, t) },
		"markdown": func(b *bytes.Buffer, t Table) error { return WriteTableMarkdown(b, t) },
		"html":     func(b *bytes.Buffer, t Table) error { return WriteTableHTML(b, t) },
		"json":     func(b *bytes.Buffer, t Table) error { return WriteTableJSON(b, t) },
	}
	for name, write := range tableWriters {
		var buf bytes.Buffer
		if err := write(&buf, table); err != nil {
			t.Fatalf("table %s writer: %v", name, err)
		}
		if buf.Len() == 0 {
			t.Errorf("table %s writer produced nothing", name)
		}
	}

	if _, ok := BuildReportTable("nonsense", ss); ok {
		t.Error("BuildReportTable accepted an unknown report type")
	}
	if IsReportType("models") != true || IsReportType("sessions") != false {
		t.Error("IsReportType does not match ReportTypes")
	}
	for _, want := range append([]string{"sessions"}, ReportTypes...) {
		if !strings.Contains(ReportTypeList(), want) {
			t.Errorf("ReportTypeList is missing %q: %s", want, ReportTypeList())
		}
	}
}

func TestBackupJSONWrapsTheDocument(t *testing.T) {
	now := time.Now()
	ss := []model.Session{sessionAgo("sess-abc", now, 0, 1, 10, 1, 0)}
	var buf bytes.Buffer
	if err := WriteBackupJSON(&buf, ss, "/tmp/sessions.db"); err != nil {
		t.Fatal(err)
	}
	var backup BackupDocument
	if err := json.Unmarshal(buf.Bytes(), &backup); err != nil {
		t.Fatalf("backup is not valid JSON: %v", err)
	}
	if backup.BackupType != "json" || backup.SourceDBPath != "/tmp/sessions.db" {
		t.Errorf("backup metadata = %q/%q, want json//tmp/sessions.db", backup.BackupType, backup.SourceDBPath)
	}
	if len(backup.Document.Sessions) != 1 || backup.Document.Sessions[0].ID != "sess-abc" {
		t.Errorf("backup document = %+v, want the one session", backup.Document.Sessions)
	}
}

func TestCopyDBCopiesBytesAndRejectsEmptySource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sessions.db")
	want := []byte("SQLite format 3\x00not-really-a-database")
	if err := os.WriteFile(src, want, 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "nested", "copy.db")
	if err := CopyDB(src, dst); err != nil {
		t.Fatalf("CopyDB: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("copied %q, want %q", got, want)
	}
	if err := CopyDB("", filepath.Join(dir, "x.db")); err == nil {
		t.Error("CopyDB accepted an empty source path")
	}
}

// ---- status: --compact and the default block must differ ----

func TestStatusRenderersDiffer(t *testing.T) {
	snap := StatusSnapshot{
		GeneratedAt: time.Date(2026, 5, 4, 9, 8, 7, 0, time.UTC),
		TodayCost:   1.5,
		MonthCost:   12.25,
		Sessions:    3,
	}
	compact := CompactStatus(snap)
	if strings.Contains(compact, "\n") {
		t.Errorf("CompactStatus must stay on one line: %q", compact)
	}
	for _, want := range []string{"Today: $1.50", "Month: $12.25", "Sessions: 3"} {
		if !strings.Contains(compact, want) {
			t.Errorf("CompactStatus is missing %q: %q", want, compact)
		}
	}

	full := FullStatus(snap)
	lines := strings.Split(full, "\n")
	if len(lines) < 4 {
		t.Errorf("FullStatus should be a multi-line block, got %d line(s): %q", len(lines), full)
	}
	for _, want := range []string{"Generated: 2026-05-04 09:08:07", "Today:     $1.50", "Month:     $12.25", "Sessions:  3"} {
		if !strings.Contains(full, want) {
			t.Errorf("FullStatus is missing %q:\n%s", want, full)
		}
	}
	// The two share their numbers; the flag must change the shape, not the data.
	for _, figure := range []string{"1.50", "12.25"} {
		if !strings.Contains(compact, figure) || !strings.Contains(full, figure) {
			t.Errorf("the two status renderers disagree about %s:\n%q\n%q", figure, compact, full)
		}
	}
}

func TestBuildStatusSnapshotSkipsHiddenAndBucketsByDay(t *testing.T) {
	now := time.Now()
	today := model.Session{ID: "t", LastActivityAt: now, CreditCost: 2}
	yesterday := model.Session{ID: "y", LastActivityAt: now.AddDate(0, 0, -1), CreditCost: 4}
	hidden := model.Session{ID: "h", LastActivityAt: now, CreditCost: 8, Hidden: true}
	snap := BuildStatusSnapshot([]model.Session{today, yesterday, hidden})
	if snap.Sessions != 2 {
		t.Errorf("Sessions = %d, want 2 (hidden sessions are not counted)", snap.Sessions)
	}
	if snap.TodayCost != 2 {
		t.Errorf("TodayCost = %v, want 2", snap.TodayCost)
	}
	if snap.MonthCost != 6 {
		t.Errorf("MonthCost = %v, want 6", snap.MonthCost)
	}
}

func TestWriteStateRoundTripsAndRejectsEmptyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "status.json")
	snap := StatusSnapshot{GeneratedAt: time.Now(), TodayCost: 1, MonthCost: 2, Sessions: 3}
	if err := WriteState(snap, path); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got StatusSnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("state file is not valid JSON: %v", err)
	}
	if got.Sessions != 3 || got.TodayCost != 1 {
		t.Errorf("round-tripped snapshot = %+v, want the written one", got)
	}
	if err := WriteState(snap, ""); err == nil {
		t.Error("WriteState accepted an empty path")
	}
}
