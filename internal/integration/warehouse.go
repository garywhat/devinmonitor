package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/i18n"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
	"github.com/garywhat/devinmonitor/internal/ui"
)

// ---- Warehouse Directory Helpers ----

// devinmonitorDir returns the base directory for devinmonitor data files.
func devinmonitorDir() string {
	if env := os.Getenv("DEVINMONITOR_CONFIG_DIR"); env != "" {
		return env
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "devinmonitor")
		}
	}
	return filepath.Join(home, ".devinmonitor")
}

func warehouseDir() string  { return filepath.Join(devinmonitorDir(), "warehouse") }
func cacheDir() string       { return filepath.Join(devinmonitorDir(), "cache") }

// ---- Persistent Cache (#73) ----

// cacheEntry is a cached aggregation result.
type cacheEntry struct {
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"createdAt"`
	Data      json.RawMessage `json:"data"`
}

// saveCache stores a cached aggregation result keyed by name.
func saveCache(name string, data interface{}) error {
	dir := cacheDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	entry := cacheEntry{
		Key:       name,
		CreatedAt: time.Now(),
		Data:      raw,
	}
	path := filepath.Join(dir, name+".json")
	out, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0644)
}

// loadCache reads a cached aggregation result. Returns nil if not found or stale.
func loadCache(name string, maxAge time.Duration) (json.RawMessage, time.Time, error) {
	path := filepath.Join(cacheDir(), name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, time.Time{}, err
	}
	if maxAge > 0 && time.Since(entry.CreatedAt) > maxAge {
		return nil, entry.CreatedAt, fmt.Errorf("cache stale")
	}
	return entry.Data, entry.CreatedAt, nil
}

// ---- Usage Warehouse (#75) ----

// warehouseSnapshot is a point-in-time usage snapshot.
type warehouseSnapshot struct {
	ID        string          `json:"id"`
	Timestamp time.Time       `json:"timestamp"`
	Summary   costSummary     `json:"summary"`
	Sessions  []model.Session `json:"sessions"`
}

var cmdWarehouse = func() *cobra.Command {
	c := &cobra.Command{
		Use:   "warehouse [snapshot|list|show <id>]",
		Short: i18n.T("cmd.warehouse"),
		Run: func(cmd *cobra.Command, args []string) {
			if len(args) == 0 {
				fmt.Fprintln(os.Stderr, "usage: warehouse [snapshot|list|show <id>]")
				os.Exit(1)
			}
			switch args[0] {
			case "snapshot":
				warehouseSnapshotCmd(cmd)
			case "list":
				warehouseListCmd()
			case "show":
				if len(args) < 2 {
					fmt.Fprintln(os.Stderr, "usage: warehouse show <id>")
					os.Exit(1)
				}
				warehouseShowCmd(args[1])
			default:
				fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
				os.Exit(1)
			}
		},
	}
	return c
}

func warehouseSnapshotCmd(cmd *cobra.Command) {
	r := openReader(cmd)
	defer r.Close()
	ss, err := r.Sessions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	snap := warehouseSnapshot{
		ID:        time.Now().Format("20060102-150405"),
		Timestamp: time.Now(),
		Summary:   computeCostSummary(ss),
		Sessions:  ss,
	}
	dir := warehouseDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir: %v\n", err)
		os.Exit(1)
	}
	path := filepath.Join(dir, snap.ID+".json")
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Snapshot saved: %s (%d sessions, $%.2f total)\n", snap.ID, len(ss), snap.Summary.TotalCost)
}

func warehouseListCmd() {
	dir := warehouseDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Printf("No snapshots found (%v)\n", err)
		return
	}
	type snap struct {
		ID        string    `json:"id"`
		Timestamp time.Time `json:"timestamp"`
		Summary   costSummary `json:"summary"`
	}
	var snaps []snap
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var s snap
		if json.Unmarshal(data, &s) == nil {
			snaps = append(snaps, s)
		}
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Timestamp.After(snaps[j].Timestamp) })
	if len(snaps) == 0 {
		fmt.Println("No snapshots found.")
		return
	}
	t := ui.NewTable("ID", "Timestamp", "Sessions", "Total Cost", "Today Cost")
	for _, s := range snaps {
		t.Row(s.ID, s.Timestamp.Format("2006-01-02 15:04:05"),
			fmt.Sprintf("%d", s.Summary.TotalSess),
			fmt.Sprintf("$%.2f", s.Summary.TotalCost),
			fmt.Sprintf("$%.2f", s.Summary.TodayCost))
	}
	fmt.Println(t.String())
}

func warehouseShowCmd(id string) {
	path := filepath.Join(warehouseDir(), id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "snapshot not found: %v\n", err)
		os.Exit(1)
	}
	var snap warehouseSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		fmt.Fprintf(os.Stderr, "parse: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Snapshot: %s\n", snap.ID)
	fmt.Printf("Timestamp: %s\n", snap.Timestamp.Format("2006-01-02 15:04:05"))
	fmt.Printf("Sessions: %d\n", snap.Summary.TotalSess)
	fmt.Printf("Total Cost: $%.2f [%s]\n", snap.Summary.TotalCost, snap.Summary.Provenance)
	fmt.Printf("Today Cost: $%.2f\n", snap.Summary.TodayCost)
	fmt.Printf("Week Cost: $%.2f\n", snap.Summary.WeekCost)
	fmt.Printf("Month Cost: $%.2f\n", snap.Summary.MonthCost)
	fmt.Printf("Active Sessions: %d\n", snap.Summary.ActiveSess)
}

// ---- File Watcher (#77) ----

// fileWatcher polls for changes to the sessions.db file by checking its
// modification time. On Windows, this is more reliable than fsnotify.
type fileWatcher struct {
	path    string
	lastMod time.Time
	onChange func()
	interval time.Duration
	stop    chan struct{}
}

// newFileWatcher creates a polling-based file watcher.
func newFileWatcher(path string, interval time.Duration, onChange func()) *fileWatcher {
	return &fileWatcher{
		path:     path,
		onChange: onChange,
		interval: interval,
		stop:     make(chan struct{}),
	}
}

func (w *fileWatcher) start() {
	go func() {
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-ticker.C:
				info, err := os.Stat(w.path)
				if err != nil {
					continue
				}
				if info.ModTime().After(w.lastMod) {
					w.lastMod = info.ModTime()
					w.onChange()
				}
			}
		}
	}()
}

func (w *fileWatcher) stopWatch() {
	close(w.stop)
}

// ---- Provenance Labels (#79) ----

// provenanceTag returns a display label for cost provenance.
// "official" = from Devin's credit/ACU accounting
// "estimated" = computed from token pricing
func provenanceTag(creditCost, acuCost, estCost float64) string {
	if creditCost > 0 || acuCost > 0 {
		return "[official]"
	}
	if estCost > 0 {
		return "[estimated]"
	}
	return ""
}

// formatCostWithProvenance formats a cost value with its provenance label.
func formatCostWithProvenance(cost float64, isFree, estimated bool) string {
	if isFree || cost == 0 {
		return "free"
	}
	s := fmt.Sprintf("$%.2f", cost)
	if estimated {
		s += " [estimated]"
	} else {
		s += " [official]"
	}
	return s
}

// Ensure report is used (for potential future cache of report rows).
var _ = report.BuildSessionRows
