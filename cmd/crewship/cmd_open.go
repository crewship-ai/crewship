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
// until you hit something better viewed visually (a chat, a Crow's Nest
// timeline, an approval queue), then jump.
//
// Resource map mirrors the web app's routing so this stays a single
// source of truth: if the routes change, the map below is the only
// thing to update.
var openCmd = &cobra.Command{
	Use:   "open <resource> [id]",
	Short: "Open the web UI page for a resource (chat, mission, journal, etc.)",
	Long: `Open the appropriate web page in your default browser.

Resources:
  dashboard                 Workspace dashboard
  agents                    Agent list
  agent     <slug-or-id>    Agent detail page
  crews                     Crew list
  crew      <slug-or-id>    Crew detail page
  chat      <chat-id>       Specific chat session
  mission   <mission-id>    Mission detail / timeline
  journal                   Live journal page
  approvals                 Approval queue
  paymaster                 Cost dashboard
  crows-nest                Live agent observation

Examples:
  crewship open chat c_abc123
  crewship open mission MIS-42
  crewship open journal
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
	require := func(n int) error {
		if len(rest) < n {
			return fmt.Errorf("%s requires %d argument(s)", resource, n)
		}
		return nil
	}
	esc := func(s string) string { return url.PathEscape(s) }

	var path string
	switch resource {
	case "dashboard", "home":
		path = "/"
	case "agents":
		path = "/agents"
	case "agent":
		if err := require(1); err != nil {
			return "", err
		}
		path = "/agents/" + esc(rest[0])
	case "crews":
		path = "/crews"
	case "crew":
		if err := require(1); err != nil {
			return "", err
		}
		path = "/crews/" + esc(rest[0])
	case "chat":
		if err := require(1); err != nil {
			return "", err
		}
		// Web UI uses ?chat=<id> on the chat page in this repo. Earlier
		// version of this code shipped `?id=<id>` which silently opened
		// the chat list without selecting the target — fixed here.
		path = "/chat?chat=" + esc(rest[0])
	case "mission":
		if err := require(1); err != nil {
			return "", err
		}
		path = "/missions/" + esc(rest[0])
	case "journal":
		path = "/journal"
	case "approvals":
		path = "/approvals"
	case "paymaster", "cost":
		path = "/paymaster"
	case "crows-nest", "crowsnest", "watch":
		if len(rest) > 0 {
			path = "/crows-nest/" + esc(rest[0])
		} else {
			path = "/crows-nest"
		}
	default:
		return "", fmt.Errorf("unknown resource %q (try: dashboard, agent, crew, chat, mission, journal, approvals, paymaster, crows-nest)", resource)
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
