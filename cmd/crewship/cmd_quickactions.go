package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
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
		fetch := func(path string, into *[]map[string]any, label string, optional bool) {
			defer wg.Done()
			var body struct {
				Data []map[string]any `json:"data"`
			}
			err := getJSON(client, path, &body)
			if err == nil {
				mu.Lock()
				*into = body.Data
				mu.Unlock()
				return
			}
			// GET /api/v1/approvals now requires roleManage (OWNER/ADMIN,
			// #2233) — a MEMBER/MANAGER gets 403 here, same as they would on
			// the web Inbox, which gates the fetch on role client-side
			// rather than showing an error for it. `optional` mirrors that:
			// no approvals visible to you is absence, not a fetch failure,
			// so it must not land in errs and render as a [partial] error
			// line on every `crewship me`/`now` a non-admin runs.
			if optional && isForbidden(err) {
				return
			}
			// Fallback: endpoint may return a bare array.
			var alt []map[string]any
			if err2 := getJSON(client, path, &alt); err2 != nil {
				recordErr(label, err)
				return
			}
			mu.Lock()
			*into = alt
			mu.Unlock()
		}
		const fanout = 3
		wg.Add(fanout)
		go fetch("/api/v1/missions?assignee=me", &missions, "missions", false)
		go fetch("/api/v1/approvals?status=pending&assignee=me", &approvals, "approvals", true)
		go fetch("/api/v1/runs?actor=me&limit=10", &runs, "runs", false)
		wg.Wait()
		// Bail with a real error (and non-zero exit) when every fetch
		// failed the same way with session_invalid — otherwise the
		// rendered dashboard becomes indistinguishable from a healthy
		// empty workspace and scripts can't detect the dead session.
		// gh#555.
		if len(errs) == fanout && allSessionInvalid(errs) {
			return errSessionExpired()
		}
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
		const fanout = 2
		wg.Add(fanout)
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
		if len(errs) == fanout && allSessionInvalid(errs) {
			return errSessionExpired()
		}
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
		fetchData := func(path string, into *[]map[string]any, label string, optional bool) {
			defer wg.Done()
			var body struct {
				Data []map[string]any `json:"data"`
			}
			err := getJSON(client, path, &body)
			if err == nil {
				mu.Lock()
				*into = body.Data
				mu.Unlock()
				return
			}
			// See the matching comment in meCmd's fetch: GET /api/v1/approvals
			// is roleManage-gated (#2233), and a MEMBER/MANAGER's 403 there is
			// absence — nothing visible to you — not a fetch failure.
			if optional && isForbidden(err) {
				return
			}
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
		}
		// Host admission control (#1668). Without this, a run held because
		// the host is short of memory is invisible here: it shows as a
		// running run that never produces anything, which is exactly what a
		// hang looks like.
		//
		// It is fetched OUTSIDE the fanout counter, and its error is held
		// aside rather than appended to errs, for two reasons. The
		// session-expired check below means "every workspace endpoint said
		// 401" — a capacity call that failed for its own reason must not be
		// able to mask an expired session. And a server older than this
		// endpoint answers 404 forever, which as a [partial] line would be
		// permanent noise on a screen people read every day.
		var (
			capacity runtimeCapacity
			capErr   error
		)
		const fanout = 3
		wg.Add(fanout + 1)
		go fetchData("/api/v1/runs?status=RUNNING&limit=20", &runningRuns, "runs", false)
		go fetchData("/api/v1/agents", &agents, "agents", false)
		go fetchData("/api/v1/approvals?status=pending", &approvals, "approvals", true)
		go func() {
			defer wg.Done()
			rc, err := fetchCapacity(client)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				capErr = err
				return
			}
			capacity = rc
		}()
		wg.Wait()
		if len(errs) == fanout && allSessionInvalid(errs) {
			return errSessionExpired()
		}
		if e := capacityFetchNoise(capErr); e != "" {
			errs = append(errs, e)
		}
		return renderNow(runningRuns, agents, approvals, capacity, errs)
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

func renderNow(runs, agents, approvals []map[string]any, capacity runtimeCapacity, errs []string) error {
	f := newFormatter()
	if f.Format == "json" || f.Format == "yaml" || f.Format == "ndjson" {
		return f.Auto(map[string]any{
			"running_runs": runs,
			"agents":       agents,
			"approvals":    approvals,
			"capacity":     capacity,
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

	// Held-for-capacity goes LAST and loud when non-empty: it is the one
	// state on this screen that explains why the numbers above are not
	// moving. `crewship capacity` has the rest.
	if held := capacityHeldLines(capacity); len(held) > 0 {
		fmt.Printf("\n%sHeld for capacity:%s %d\n", cli.Bold, cli.Reset, len(held))
		for _, l := range held {
			fmt.Printf("  %s%s%s\n", cli.Yellow, l, cli.Reset)
		}
		fmt.Printf("  %s(these runs are queued, not hung — `crewship capacity` for the thresholds)%s\n", cli.Dim, cli.Reset)
	}

	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "%s[partial]%s %s\n", cli.Dim, cli.Reset, e)
	}
	return nil
}

// isForbidden reports whether err is a 403 from the API — used by the
// quick-action fanout fetchers to tell "you're not allowed to see this"
// (e.g. a MEMBER/MANAGER against roleManage-gated GET /api/v1/approvals,
// #2233) apart from a real fetch failure. The former should render as
// absence; only the latter belongs in the `[partial]` error list.
func isForbidden(err error) bool {
	var apiErr *cli.APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden
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

// allSessionInvalid reports whether every aggregated fetch in the
// quick-action commands (now / me / today) failed with the same
// "session expired" auth shape — i.e. all of them, none of them
// partial. When that's the case the dashboard view becomes
// indistinguishable from "all clear" (gh#555): zero missions, zero
// runs, zero approvals reads the same as a healthy quiet workspace.
// The fix is to bail with a clear re-login hint instead of rendering
// an empty dashboard and exiting 0.
//
// Partial failure stays a soft `[partial]` warning so the user keeps
// whatever data did succeed — that's the existing behaviour and
// continues to be the right call when only one endpoint is unhappy.
func allSessionInvalid(errs []string) bool {
	if len(errs) == 0 {
		return false
	}
	for _, e := range errs {
		if !strings.Contains(e, "(401)") || !strings.Contains(e, "session_invalid") {
			return false
		}
	}
	return true
}

// errSessionExpired is the user-facing error returned when the auth
// session is dead across every aggregated call. The remediation —
// `crewship login` — is named in the message so scripts and humans
// have a single, actionable next step. Exit code is non-zero via
// Cobra's standard error path; that's the gh#555 fix.
func errSessionExpired() error {
	return fmt.Errorf("session expired — run `crewship login` to re-authenticate")
}

func init() {
	rootCmd.AddCommand(meCmd, todayCmd, nowCmd)
}
