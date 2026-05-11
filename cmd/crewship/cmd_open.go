package main

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// openCmd opens the relevant web-UI page for a CLI resource in the user's
// default browser. Hybrid CLI/web workflow: do everything in the terminal
// until you hit something better viewed visually (a chat, a mission
// timeline, an approval queue), then jump.
//
// Resource map mirrors the web app's routing so this stays a single
// source of truth: if the routes change, the map below is the only
// thing to update.
var openCmd = &cobra.Command{
	Use:   "open <resource> [id]",
	Short: "Open the web UI page for a resource (inbox, activity, chat, etc.)",
	Long: `Open the appropriate web page in your default browser.

Resources:
  inbox                     Daily-driver inbox (default landing)
  activity                  Activity feed
  agents                    Agent list
  agent     <slug-or-id>    Agent detail page
  crews                     Crew list
  crew      <slug-or-id>    Crew detail page
  chat      <agent-slug>    Chat with a specific agent
  mission   <mission-id>    Mission timeline
  journal                   Live journal page
  approvals                 Approval queue
  integrations              Installed integrations
  routines                  Scheduled routines
  issues                    Issue tracker
  runs                      Run history
  settings                  Workspace settings
  admin                     Admin console
  audit                     Audit log
  credentials               Credential vault

Examples:
  crewship open inbox
  crewship open chat viktor
  crewship open mission MIS-42
  crewship open --print-only mission MIS-42   # print URL, don't open`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// open intentionally does NOT requireAuth — we're just resolving a URL.
		// The browser will prompt for login if the user isn't already.
		base := cli.ResolveServer(flagServer, cliCfg)
		u, err := buildOpenURL(base, args)
		if err != nil {
			return err
		}
		if printOnly, _ := cmd.Flags().GetBool("print-only"); printOnly {
			fmt.Println(u)
			return nil
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "%sopening %s%s\n", cli.Dim, u, cli.Reset)
		return browserOpen(u)
	},
}

// buildOpenURL turns args into a fully-qualified URL against `base`.
// Centralised so the URL templates have one home. Keep IDs URL-escaped
// to handle the rare slug with `:` or other reserved characters.
func buildOpenURL(base string, args []string) (string, error) {
	resource := strings.ToLower(args[0])
	rest := args[1:]
	requireExact := func(n int) error {
		if len(rest) != n {
			return fmt.Errorf("%s requires exactly %d argument(s), got %d", resource, n, len(rest))
		}
		return nil
	}
	requireRange := func(min, max int) error {
		if len(rest) < min || len(rest) > max {
			return fmt.Errorf("%s accepts %d-%d argument(s), got %d", resource, min, max, len(rest))
		}
		return nil
	}
	esc := func(s string) string { return url.PathEscape(s) }

	var path string
	switch resource {
	case "dashboard", "home", "inbox":
		// `dashboard`/`home` are kept as aliases for back-compat but route
		// to the actual post-auth landing surface, /inbox.
		path = "/inbox"
	case "activity":
		path = "/activity"
	case "agents":
		path = "/agents"
	case "agent":
		if err := requireExact(1); err != nil {
			return "", err
		}
		path = "/agents/" + esc(rest[0])
	case "crews":
		path = "/crews"
	case "crew":
		if err := requireExact(1); err != nil {
			return "", err
		}
		path = "/crews/" + esc(rest[0])
	case "chat":
		if err := requireExact(1); err != nil {
			return "", err
		}
		// Chat is per-agent: /chat/<agent-slug>. The earlier version of
		// this code routed to /chat?chat=<id> which targeted a chat-list
		// page that doesn't exist in this app.
		path = "/chat/" + esc(rest[0])
	case "mission":
		if err := requireExact(1); err != nil {
			return "", err
		}
		// Mission detail lives under the timeline sub-route in this app.
		path = "/missions/" + esc(rest[0]) + "/timeline"
	case "journal":
		path = "/journal"
	case "approvals":
		path = "/approvals"
	case "integrations":
		path = "/integrations"
	case "routines":
		path = "/routines"
	case "issues":
		if err := requireRange(0, 1); err != nil {
			return "", err
		}
		if len(rest) == 1 {
			path = "/issues/" + esc(rest[0])
		} else {
			path = "/issues"
		}
	case "runs":
		path = "/runs"
	case "settings":
		path = "/settings"
	case "admin":
		path = "/admin"
	case "audit":
		path = "/audit"
	case "credentials":
		path = "/credentials"
	default:
		return "", fmt.Errorf("unknown resource %q (try: inbox, activity, agent[s], crew[s], chat, mission, journal, approvals, integrations, routines, issues, runs, settings, admin, audit, credentials)", resource)
	}
	return strings.TrimRight(base, "/") + path, nil
}

// browserOpen launches the user's default browser. Cross-platform via the
// well-known per-OS opener: open (macOS), xdg-open (Linux), rundll32
// (Windows). The Windows path uses cmd /c start so spaces in the URL
// don't break.
//
// Design note: we deliberately use exec rather than a Go library so this
// stays dependency-free.
func browserOpen(u string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", u)
	case "windows":
		c = exec.Command("cmd", "/c", "start", "", u)
	default:
		c = exec.Command("xdg-open", u)
	}
	return c.Start()
}

func init() {
	openCmd.Flags().Bool("print-only", false, "Print the URL instead of opening the browser")
}
