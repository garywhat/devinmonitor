package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/reader"
	"github.com/garywhat/devinmonitor/internal/report"
)

// ---- Web Dashboard (#84) ----

var cmdWeb = func() *cobra.Command {
	var port int
	c := &cobra.Command{
		Use:   "web",
		Short: "Start local web dashboard with charts and SSE live updates",
		Run: func(cmd *cobra.Command, args []string) {
			runWebServer(cmd, port)
		},
	}
	c.Flags().IntVar(&port, "port", 8080, "port to listen on")
	return c
}

// webState holds SSE subscriber channels.
type webState struct {
	mu          sync.Mutex
	subscribers map[chan string]bool
	dataDir     string
}

func runWebServer(cmd *cobra.Command, port int) {
	dataDir := dataDirFrom(cmd)
	st := &webState{
		subscribers: make(map[chan string]bool),
		dataDir:     dataDir,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", st.handleIndex)
	mux.HandleFunc("/api/sessions", st.handleSessions)
	mux.HandleFunc("/api/cost-summary", st.handleCostSummary)
	mux.HandleFunc("/api/alerts", st.handleAlerts)
	mux.HandleFunc("/sse", st.handleSSE)

	// Background poller: periodically refreshes data and notifies SSE subscribers.
	go st.pollLoop()

	addr := fmt.Sprintf(":%d", port)
	fmt.Fprintf(os.Stderr, "DevinMonitor web dashboard: http://localhost:%d\n", port)
	fmt.Fprintf(os.Stderr, "Press Ctrl+C to stop.\n")

	srv := &http.Server{Addr: addr, Handler: mux}

	// Graceful shutdown on Ctrl+C or SIGTERM.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt)
		<-sigCh
		fmt.Fprintf(os.Stderr, "\nShutting down web server...\n")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "web server error: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Web server stopped.\n")
}

func (st *webState) loadSessions() ([]model.Session, error) {
	r, err := reader.Open(st.dataDir)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.Sessions()
}

func (st *webState) pollLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		data := st.snapshotJSON()
		st.mu.Lock()
		for ch := range st.subscribers {
			select {
			case ch <- data:
			default: // drop if subscriber is slow
			}
		}
		st.mu.Unlock()
	}
}

func (st *webState) snapshotJSON() string {
	ss, err := st.loadSessions()
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	sum := computeCostSummary(ss)
	rows := report.BuildSessionRows(ss)
	type snapshot struct {
		Summary  costSummary              `json:"summary"`
		Sessions []report.SessionRow     `json:"sessions"`
		Alerts   []model.AlertItem       `json:"alerts"`
	}
	snap := snapshot{Summary: sum, Sessions: rows, Alerts: detectAlerts(ss)}
	data, _ := json.Marshal(snap)
	return string(data)
}

func (st *webState) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, webDashboardHTML)
}

func (st *webState) handleSessions(w http.ResponseWriter, r *http.Request) {
	ss, err := st.loadSessions()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	rows := report.BuildSessionRows(ss)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)
}

func (st *webState) handleCostSummary(w http.ResponseWriter, r *http.Request) {
	ss, err := st.loadSessions()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	sum := computeCostSummary(ss)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sum)
}

func (st *webState) handleAlerts(w http.ResponseWriter, r *http.Request) {
	ss, err := st.loadSessions()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	alerts := detectAlerts(ss)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(alerts)
}

func (st *webState) handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan string, 1)
	st.mu.Lock()
	st.subscribers[ch] = true
	st.mu.Unlock()
	defer func() {
		st.mu.Lock()
		delete(st.subscribers, ch)
		st.mu.Unlock()
	}()

	// Send initial snapshot.
	fmt.Fprintf(w, "data: %s\n\n", st.snapshotJSON())
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	for {
		select {
		case data := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", data)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		case <-r.Context().Done():
			return
		}
	}
}

const webDashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>DevinMonitor Dashboard</title>
<style>
  body { font-family: monospace; background: #1a1a2e; color: #e0e0e0; margin: 20px; }
  h1 { color: #7c7cff; }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 12px; margin: 20px 0; }
  .card { background: #16213e; border-radius: 8px; padding: 16px; border: 1px solid #233; }
  .card .label { color: #888; font-size: 12px; }
  .card .value { font-size: 24px; color: #ffd700; }
  table { width: 100%; border-collapse: collapse; margin: 20px 0; }
  th, td { text-align: left; padding: 6px 10px; border-bottom: 1px solid #233; }
  th { color: #7c7cff; }
  canvas { margin: 10px 0; }
  #alerts { color: #ff6b6b; }
</style>
</head>
<body>
<h1>DevinMonitor Dashboard</h1>
<div class="grid" id="kpis"></div>
<canvas id="chart" width="800" height="200"></canvas>
<h2>Sessions</h2>
<table id="sessions"><thead><tr>
  <th>ID</th><th>Title</th><th>Model</th><th>Project</th><th>Cost</th><th>Tokens</th>
</tr></thead><tbody></tbody></table>
<h2>Alerts</h2>
<div id="alerts"></div>
<script>
const es = new EventSource('/sse');
es.onmessage = function(e) {
  const d = JSON.parse(e.data);
  if (d.error) return;
  // KPIs
  const kpis = document.getElementById('kpis');
  kpis.innerHTML = '';
  const items = [
    ['Active', d.summary.activeSessions],
    ['Today', '$' + d.summary.todayCost.toFixed(2)],
    ['Week', '$' + d.summary.weekCost.toFixed(2)],
    ['Month', '$' + d.summary.monthCost.toFixed(2)],
    ['Total', '$' + d.summary.totalCost.toFixed(2)],
    ['Sessions', d.summary.totalSessions],
  ];
  items.forEach(([l, v]) => {
    const c = document.createElement('div');
    c.className = 'card';
    c.innerHTML = '<div class="label">' + l + '</div><div class="value">' + v + '</div>';
    kpis.appendChild(c);
  });
  // Sessions table
  const tb = document.querySelector('#sessions tbody');
  tb.innerHTML = '';
  (d.sessions || []).slice(0, 20).forEach(s => {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td>' + s.ID + '</td><td>' + esc(s.Title) + '</td><td>' + esc(s.Model) +
      '</td><td>' + esc(s.Project) + '</td><td>$' + s.Cost.toFixed(2) +
      '</td><td>' + (s.InputTok + s.OutputTok) + '</td>';
    tb.appendChild(tr);
  });
  // Alerts
  const al = document.getElementById('alerts');
  al.innerHTML = '';
  (d.alerts || []).forEach(a => {
    const div = document.createElement('div');
    div.textContent = '[' + a.Severity + '] ' + a.Message;
    al.appendChild(div);
  });
  // Chart
  drawChart(d.sessions || []);
};

function drawChart(sessions) {
  const cv = document.getElementById('chart');
  const ctx = cv.getContext('2d');
  ctx.clearRect(0, 0, cv.width, cv.height);
  if (sessions.length === 0) return;
  const costs = sessions.slice(0, 30).map(s => s.Cost).reverse();
  const max = Math.max(...costs, 1);
  const bw = cv.width / costs.length;
  ctx.fillStyle = '#7c7cff';
  costs.forEach((c, i) => {
    const h = (c / max) * (cv.height - 20);
    ctx.fillRect(i * bw + 2, cv.height - h - 10, bw - 4, h);
  });
}

function esc(s) {
  const d = document.createElement('div');
  d.textContent = s || '';
  return d.innerHTML;
}
</script>
</body>
</html>`
