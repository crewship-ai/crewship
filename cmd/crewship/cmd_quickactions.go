package main

import (
	"fmt"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Quick-action commands (`me`, `today`, `now`) compose existing REST
// endpoints into single-screen workflow shortcuts.
//
// Goal: ~one HTTP round trip's wall time, even when 3-5 endpoints back
// the view. Each command uses a fan-out fetcher (sync.WaitGroup +
// best-effort error handling) so any one slow endpoint doesn't block
// the rest of the screen — partial views are better than blank ones.

// meCmd: "what's mine?" — missions assigned to me + pending approvals
// + my recent activity. Used as the "I just sat down at the laptop"
// screen.
var meCmd = &cobra.Command{
	Use:   "me",
	Short: "What's on your plate right now",
	Long: `Single-screen view of your current workload:
  - Missions assigned to you
  - Approvals waiting on you
  - Your recent run activity

Equivalent to running 'mission list --assignee=me' + 'approvals list
--status=pending' + 'history --actor=me' and stitching the output.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var (
			missions  []map[string]any
			approvals []map[string]any
			runs      []map[string]any
			mu        sync.Mutex
			wg        sync.WaitGroup
			errs      []string
		)
		recordErr := func(label string, err error) {
			mu.Lock()
			errs = append(errs, label+": "+err.Error())
			mu.Unlock()
		}
		fetch := func(path string, into *[]map[string]any, label string) {
			defer wg.Done()
			var body struct {
				Data []map[string]any `json:"data"`
			}
			if err := getJSON(client, path, &body); err != nil {
				// Fallback: endpoint may return a bare array.
				var alt []map[string]any
				if err2 := getJSON(client, path, &alt); err2 != nil {
					recordErr(label, err)
					return
				}
				mu.Lock()
				*into = alt
				mu.Unlock()
				return
			}
			mu.Lock()
			*into = body.Data
			mu.Unlock()
		}
		wg.Add(3)
		go fetch("/api/v1/missions?assignee=me", &missions, "missions")
		go fetch("/api/v1/approvals?status=pending&assignee=me", &approvals, "approvals")
		go fetch("/api/v1/runs?actor=me&limit=10", &runs, "runs")
		wg.Wait()
		return renderMe(missions, approvals, runs, errs)
	},
}

// todayCmd: today's runs + today's spend. The "what happened today"
// view, useful for retrospectives and standups.
var todayCmd = &cobra.Command{
	Use:   "today",
	Short: "Today's runs and spend across the workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var (
			runs []map[string]any
			cost map[string]any
			mu   sync.Mutex
			wg   sync.WaitGroup
			errs []string
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			var body struct {
				Data []map[string]any `json:"data"`
			}
			q := url.Values{}
			q.Set("limit", "100")
			if err := getJSON(client, "/api/v1/runs?"+q.Encode(), &body); err != nil {
				mu.Lock()
				errs = append(errs, "runs: "+err.Error())
				mu.Unlock()
				return
			}
			// Filter to today UTC client-side. Cheap on N≤100.
			today := time.Now().UTC().Format("2006-01-02")
			filtered := body.Data[:0]
			for _, r := range body.Data {
				if ts, _ := r["created_at"].(string); len(ts) >= 10 && ts[:10] == today {
					filtered = append(filtered, r)
				}
			}
			mu.Lock()
			runs = filtered
			mu.Unlock()
		}()
		go func() {
			defer wg.Done()
			if err := getJSON(client, "/api/v1/paymaster/top-spenders?range=24h&limit=5", &cost); err != nil {
				mu.Lock()
				errs = append(errs, "cost: "+err.Error())
				mu.Unlock()
			}
		}()
		wg.Wait()
		return renderToday(runs, cost, errs)
	},
}

// nowCmd: live state — running missions, idle agents, pending
// approvals. The "live status board" screen.
var nowCmd = &cobra.Command{
	Use:   "now",
	Short: "Live status: running missions, idle agents, pending approvals",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var (
			runningRuns []map[string]any
			agents      []map[string]any
			approvals   []map[string]any
			mu          sync.Mutex
			wg          sync.WaitGroup
			errs        []string
		)
		fetchData := func(path string, into *[]map[string]any, label string) {
			defer wg.Done()
			var body struct {
				Data []map[string]any `json:"data"`
			}
			if err := getJSON(client, path, &body); err != nil {
				var alt []map[string]any
				if err2 := getJSON(client, path, &alt); err2 != nil {
					mu.Lock()
					errs = append(errs, label+": "+err.Error())
					mu.Unlock()
					return
				}
				mu.Lock()
				*into = alt
				mu.Unlock()
				return
			}
			mu.Lock()
			*into = body.Data
			mu.Unlock()
		}
		wg.Add(3)
		go fetchData("/api/v1/runs?status=RUNNING&limit=20", &runningRuns, "runs")
		go fetchData("/api/v1/agents", &agents, "agents")
		go fetchData("/api/v1/approvals?status=pending", &approvals, "approvals")
		wg.Wait()
		return renderNow(runningRuns, agents, approvals, errs)
	},
}

func renderMe(missions, approvals, runs []map[string]any, errs []string) error {
	f := newFormatter()
	if f.Format == "json" || f.Format == "yaml" || f.Format == "ndjson" {
		v := map[string]any{
			"missions":  missions,
			"approvals": approvals,
			"runs":      runs,
			"errors":    errs,
		}
		return f.Auto(v, nil, nil)
	}
	fmt.Printf("%s━━ Your missions ━━%s  (%d)\n", cli.Bold, cli.Reset, len(missions))
	for _, m := range missions {
		fmt.Printf("  %s • %s\n", str(m["id"]), str(m["title"]))
	}
	fmt.Printf("\n%s━━ Approvals waiting on you ━━%s  (%d)\n", cli.Bold, cli.Reset, len(approvals))
	for _, a := range approvals {
		fmt.Printf("  %s • %s  %s%s%s\n", str(a["id"]), str(a["title"]), cli.Yellow, str(a["status"]), cli.Reset)
	}
	fmt.Printf("\n%s━━ Your recent runs ━━%s  (%d)\n", cli.Bold, cli.Reset, len(runs))
	for _, r := range runs {
		fmt.Printf("  %s • agent=%s  status=%s\n", str(r["id"]), str(r["agent_slug"]), str(r["status"]))
	}
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "%s[partial]%s %s\n", cli.Dim, cli.Reset, e)
	}
	return nil
}

func renderToday(runs []map[string]any, cost map[string]any, errs []string) error {
	f := newFormatter()
	if f.Format == "json" || f.Format == "yaml" || f.Format == "ndjson" {
		return f.Auto(map[string]any{
			"runs":   runs,
			"cost":   cost,
			"errors": errs,
		}, nil, nil)
	}
	fmt.Printf("%sToday%s  (%s UTC)\n\n", cli.Bold, cli.Reset, time.Now().UTC().Format("2006-01-02"))
	fmt.Printf("%sRuns:%s %d\n", cli.Bold, cli.Reset, len(runs))
	statusCount := map[string]int{}
	for _, r := range runs {
		statusCount[str(r["status"])]++
	}
	for s, n := range statusCount {
		fmt.Printf("  %s × %d\n", s, n)
	}
	if cost != nil {
		fmt.Printf("\n%sCost (last 24h):%s\n", cli.Bold, cli.Reset)
		// cost shape varies — best-effort top spender summary.
		fmt.Printf("  (run 'crewship cost' for full breakdown)\n")
	}
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "%s[partial]%s %s\n", cli.Dim, cli.Reset, e)
	}
	return nil
}

func renderNow(runs, agents, approvals []map[string]any, errs []string) error {
	f := newFormatter()
	if f.Format == "json" || f.Format == "yaml" || f.Format == "ndjson" {
		return f.Auto(map[string]any{
			"running_runs": runs,
			"agents":       agents,
			"approvals":    approvals,
			"errors":       errs,
		}, nil, nil)
	}
	now := time.Now().UTC().Format("15:04:05 UTC")
	fmt.Printf("%sNow%s  %s\n\n", cli.Bold, cli.Reset, now)

	fmt.Printf("%sRunning missions/runs:%s %d\n", cli.Bold, cli.Reset, len(runs))
	for _, r := range runs {
		fmt.Printf("  %s • agent=%s  started=%s\n", str(r["id"]), str(r["agent_slug"]), str(r["started_at"]))
	}
	idle, busy := 0, 0
	for _, a := range agents {
		if s := str(a["status"]); s == "running" || s == "RUNNING" || s == "busy" {
			busy++
		} else {
			idle++
		}
	}
	fmt.Printf("\n%sAgents:%s %d idle, %d busy\n", cli.Bold, cli.Reset, idle, busy)
	fmt.Printf("\n%sPending approvals:%s %d\n", cli.Bold, cli.Reset, len(approvals))
	for _, a := range approvals {
		fmt.Printf("  %s • %s\n", str(a["id"]), str(a["title"]))
	}
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "%s[partial]%s %s\n", cli.Dim, cli.Reset, e)
	}
	return nil
}

// str safely coerces an interface{} → string (for map[string]any reads).
func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func init() {
	rootCmd.AddCommand(meCmd, todayCmd, nowCmd)
}
