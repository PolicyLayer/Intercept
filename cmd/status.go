package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/policylayer/intercept/internal/events"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show a summary of intercept proxy instances and recent activity",
	RunE:  runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

// instanceInfo tracks the state of a single intercept proxy instance,
// aggregated from event log entries.
type instanceInfo struct {
	ID           string
	Server       string
	PID          int
	StateBackend string
	FailMode     string
	StartedAt    time.Time
	LastSeen     time.Time
	ShutDown     bool
}

// statusData aggregates all event log data for the status report.
type statusData struct {
	Instances  map[string]*instanceInfo
	ToolCalls  []events.Event
	Denied     []events.Event
	FirstEvent time.Time
	LastEvent  time.Time
}

// runStatus reads event log files from ~/.intercept/events, aggregates instance
// and tool call data, and prints a summary table to stdout.
func runStatus(cmd *cobra.Command, args []string) error {
	eventsDir, err := interceptSubdir("events")
	if err != nil {
		return fmt.Errorf("resolving events directory: %w", err)
	}

	if err := events.PruneOldFiles(eventsDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: event cleanup failed: %v\n", err)
	}

	matches, _ := filepath.Glob(filepath.Join(eventsDir, "*.jsonl"))
	if len(matches) == 0 {
		fmt.Printf("No event data found in %s\n", eventsDir)
		return nil
	}

	data := &statusData{
		Instances: make(map[string]*instanceInfo),
	}
	for _, path := range matches {
		data.loadFile(path)
	}

	now := time.Now()

	// Print Instances
	sorted := make([]*instanceInfo, 0, len(data.Instances))
	for _, inst := range data.Instances {
		sorted = append(sorted, inst)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].StartedAt.After(sorted[j].StartedAt)
	})

	fmt.Println("Instances")
	tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "  ID\tServer\tPID\tState Backend\tFail Mode\tStatus\tLast Seen\n")
	for _, inst := range sorted {
		status := resolveStatus(inst, now)
		backend := displayOrDash(inst.StateBackend)
		failMode := displayOrDash(inst.FailMode)
		fmt.Fprintf(tw, "  %s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			inst.ID, inst.Server, inst.PID,
			backend, failMode,
			status, formatAge(now, inst.LastSeen))
	}
	tw.Flush()

	// Stats
	totalCalls := len(data.ToolCalls)
	totalDenied := len(data.Denied)

	if totalCalls > 0 {
		fmt.Println()
		span := data.LastEvent.Sub(data.FirstEvent)
		fmt.Printf("Stats (%s)\n", formatSpan(span))

		denyPct := float64(totalDenied) / float64(totalCalls) * 100

		cutoff := now.Add(-5 * time.Minute)
		recentCalls := 0
		for i := len(data.ToolCalls) - 1; i >= 0; i-- {
			if data.ToolCalls[i].Ts.Before(cutoff) {
				break
			}
			recentCalls++
		}
		callsPerMin := float64(recentCalls) / 5.0

		tw2 := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintf(tw2, "  Total calls:\t%d\n", totalCalls)
		if totalDenied > 0 {
			fmt.Fprintf(tw2, "  Denied:\t%d (%.1f%%)\n", totalDenied, denyPct)
		} else {
			fmt.Fprintf(tw2, "  Denied:\t0\n")
		}
		fmt.Fprintf(tw2, "  Calls/min:\t%.1f\n", callsPerMin)
		tw2.Flush()
	}

	// Top Deny Rules
	if len(data.Denied) > 0 {
		ruleCounts := make(map[string]int)
		for _, ev := range data.Denied {
			key := ev.Rule
			if key == "" {
				key = ev.Message
			}
			if key != "" {
				ruleCounts[key]++
			}
		}

		if len(ruleCounts) > 0 {
			type ruleCount struct {
				Rule  string
				Count int
			}
			topRules := make([]ruleCount, 0, len(ruleCounts))
			for rule, count := range ruleCounts {
				topRules = append(topRules, ruleCount{Rule: rule, Count: count})
			}
			sort.Slice(topRules, func(i, j int) bool {
				return topRules[i].Count > topRules[j].Count
			})
			if len(topRules) > 10 {
				topRules = topRules[:10]
			}

			fmt.Println()
			fmt.Println("Top Deny Rules")
			tw3 := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintf(tw3, "  Rule\tCount\n")
			for _, rc := range topRules {
				fmt.Fprintf(tw3, "  %s\t%d\n", rc.Rule, rc.Count)
			}
			tw3.Flush()
		}

		// Recent Denied Calls (last 10)
		fmt.Println()
		fmt.Println("Recent Denied Calls")
		tw4 := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintf(tw4, "  Time\tTool\tRule\n")
		limit := min(len(data.Denied), 10)
		for i := len(data.Denied) - 1; i >= len(data.Denied)-limit; i-- {
			ev := data.Denied[i]
			rule := ev.Rule
			if rule == "" {
				rule = ev.Message
			}
			fmt.Fprintf(tw4, "  %s\t%s\t%s\n", ev.Ts.Local().Format("15:04:05"), ev.Tool, rule)
		}
		tw4.Flush()
	}

	return nil
}

// loadFile reads a single JSONL event file and updates the statusData
// with instance state and tool call records.
func (s *statusData) loadFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var ev events.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}

		if s.FirstEvent.IsZero() || ev.Ts.Before(s.FirstEvent) {
			s.FirstEvent = ev.Ts
		}
		if ev.Ts.After(s.LastEvent) {
			s.LastEvent = ev.Ts
		}

		inst, ok := s.Instances[ev.Instance]
		if !ok {
			inst = &instanceInfo{ID: ev.Instance}
			s.Instances[ev.Instance] = inst
		}

		switch ev.Type {
		case "startup":
			inst.Server = ev.Server
			inst.PID = ev.PID
			inst.StateBackend = maskDSN(ev.StateBackend)
			inst.FailMode = ev.FailMode
			inst.StartedAt = ev.Ts
			inst.LastSeen = ev.Ts
		case "tool_call":
			s.ToolCalls = append(s.ToolCalls, ev)
			if ev.Result == "denied" {
				s.Denied = append(s.Denied, ev)
			}
			inst.LastSeen = ev.Ts
		case "heartbeat":
			inst.LastSeen = ev.Ts
		case "shutdown":
			inst.ShutDown = true
			inst.LastSeen = ev.Ts
		case "config_reload":
			inst.LastSeen = ev.Ts
		}
	}
}

// resolveStatus determines whether an instance is alive, dead, or unknown
// based on its shutdown flag and the time since its last heartbeat.
func resolveStatus(inst *instanceInfo, now time.Time) string {
	if inst.ShutDown {
		return "dead"
	}
	if now.Sub(inst.LastSeen) <= 90*time.Second {
		return "alive"
	}
	return "unknown"
}

// formatAge returns a human-readable duration string like "5m ago" or "2d ago".
func formatAge(now time.Time, t time.Time) string {
	d := max(now.Sub(t), 0)

	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(math.Round(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// formatSpan returns a human-readable label for a time span like "last 5m".
func formatSpan(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("last %dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("last %dh", int(d.Hours()))
	default:
		return fmt.Sprintf("last %dd", int(d.Hours()/24))
	}
}

// displayOrDash returns s if non-empty, or "-" as a placeholder.
func displayOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// maskDSN shortens a DSN to just its scheme and masked host for display.
// "redis://user:pass@host:6379/0" becomes "redis://host:6379".
// Non-URL values like "sqlite" are returned as-is.
func maskDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return dsn
	}
	return u.Scheme + "://" + u.Host
}
