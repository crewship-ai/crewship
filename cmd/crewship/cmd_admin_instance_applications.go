//go:build !clionly

package main

// `crewship admin instance applications` — Word, Sheet and agents in them for
// the whole instance (GET/PATCH /api/v1/admin/instance/artifacts/applications,
// POST …/{app}/diagnostics), #5315.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

const craftApplicationsPath = "/api/v1/admin/instance/artifacts/applications"

type adminCraftComponent struct {
	Available      bool   `json:"available" yaml:"available"`
	Reason         string `json:"reason,omitempty" yaml:"reason,omitempty"`
	Development    bool   `json:"development,omitempty" yaml:"development,omitempty"`
	Version        string `json:"version,omitempty" yaml:"version,omitempty"`
	EngineRevision string `json:"engine_revision,omitempty" yaml:"engine_revision,omitempty"`
	EngineSHA256   string `json:"engine_sha256,omitempty" yaml:"engine_sha256,omitempty"`
	EditorBundle   string `json:"editor_bundle,omitempty" yaml:"editor_bundle,omitempty"`
	Runtime        string `json:"runtime,omitempty" yaml:"runtime,omitempty"`
	Detail         string `json:"detail,omitempty" yaml:"detail,omitempty"`
}

type adminCraftApplication struct {
	ID         string               `json:"id" yaml:"id"`
	Label      string               `json:"label" yaml:"label"`
	Desired    string               `json:"desired" yaml:"desired"`
	Source     string               `json:"source" yaml:"source"`
	Locked     bool                 `json:"locked" yaml:"locked"`
	State      string               `json:"state" yaml:"state"`
	Reason     string               `json:"reason,omitempty" yaml:"reason,omitempty"`
	Message    string               `json:"message,omitempty" yaml:"message,omitempty"`
	DrainUntil *time.Time           `json:"drain_until,omitempty" yaml:"drain_until,omitempty"`
	UpdatedBy  string               `json:"updated_by,omitempty" yaml:"updated_by,omitempty"`
	UpdatedAt  *time.Time           `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
	Component  *adminCraftComponent `json:"component,omitempty" yaml:"component,omitempty"`
	Documents  *int                 `json:"documents,omitempty" yaml:"documents,omitempty"`
}

type adminCraftApplications struct {
	Applications []adminCraftApplication `json:"applications" yaml:"applications"`
	Agents       adminCraftApplication   `json:"agents" yaml:"agents"`
	Editor       string                  `json:"editor" yaml:"editor"`
}

type adminCraftDiagnostics struct {
	ID         string `json:"id" yaml:"id"`
	Passed     bool   `json:"passed" yaml:"passed"`
	Reason     string `json:"reason,omitempty" yaml:"reason,omitempty"`
	Message    string `json:"message,omitempty" yaml:"message,omitempty"`
	Runtime    string `json:"runtime,omitempty" yaml:"runtime,omitempty"`
	EngineSHA  string `json:"engine_sha256,omitempty" yaml:"engine_sha256,omitempty"`
	Version    string `json:"version,omitempty" yaml:"version,omitempty"`
	DurationMS int64  `json:"duration_ms" yaml:"duration_ms"`
	Editor     string `json:"editor" yaml:"editor"`
}

// craftSubject maps the names people use to the API's identifiers.
func craftSubject(name string, agents bool) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "word", "wordcraft":
		return "wordcraft", nil
	case "sheet", "gridcraft":
		return "gridcraft", nil
	case "agents":
		if agents {
			return "agents", nil
		}
	}
	want := "word or sheet"
	if agents {
		want = "word, sheet or agents"
	}
	return "", cli.WithExitCode(fmt.Errorf("unknown application %q: use %s", name, want), cli.ExitValidation)
}

var adminInstanceApplicationsCmd = &cobra.Command{
	Use:     "applications",
	Aliases: []string{"apps"},
	Short:   "Word, Sheet and agents in them, for the whole instance",
	Long: `The Word and Sheet applications are part of Crewship. An instance
administrator turns each one on or off for every workspace, and separately
allows agents to work in them. Both applications and agents start off.

Turning an application off never deletes or changes a document. People can
still open, read and export every document; nobody can create one or start
editing. By default editors that are already open get 15 minutes to finish
and save (--drain), --now closes at once.

  crewship admin instance applications                 show
  crewship admin instance applications enable word     turn Word on
  crewship admin instance applications disable sheet   turn Sheet off
  crewship admin instance applications enable agents   allow agents
  crewship admin instance applications doctor word     self-test Word
  crewship admin instance applications install word --file crewship-craft-wordcraft.craftpkg

Word and Sheet are not part of the Crewship binary. Turning one on downloads
the package this release trusts, verifies it and starts it without a
restart; offline, install the same package from a file.`,
	Args: cobra.NoArgs,
	RunE: showAdminApplications,
}

func showAdminApplications(cmd *cobra.Command, _ []string) error {
	client, err := requireAuthInstance()
	if err != nil {
		return err
	}
	var out adminCraftApplications
	if err := getJSON(client, craftApplicationsPath, &out); err != nil {
		return err
	}
	return printAdminApplications(cmd, out)
}

func printAdminApplications(cmd *cobra.Command, out adminCraftApplications) error {
	return resolvedFormatter(cmd).AutoHuman(out, func() {
		w := cmd.OutOrStdout()
		line := func(a adminCraftApplication) {
			state := a.State
			if a.State == "draining" && a.DrainUntil != nil {
				state += " until " + a.DrainUntil.Local().Format("15:04")
			}
			extra := ""
			if a.Locked {
				extra = " (fixed by server configuration)"
			}
			if a.Documents != nil {
				extra += fmt.Sprintf(", %d documents", *a.Documents)
			}
			fmt.Fprintf(w, "%-26s %-9s %s%s\n", a.Label, a.Desired, state, extra)
			if a.Message != "" {
				fmt.Fprintf(w, "  %s\n", a.Message)
			}
			if c := a.Component; c != nil && c.Available {
				kind := "installed"
				if c.Development {
					kind = "development override"
				}
				version := c.Version
				if version == "" {
					version = c.EngineRevision
				}
				fmt.Fprintf(w, "  %s, %s %s\n", kind, c.Runtime, version)
			}
			if c := a.Component; c != nil && c.Detail != "" {
				fmt.Fprintf(w, "  last error: %s\n", c.Detail)
			}
		}
		for _, a := range out.Applications {
			line(a)
		}
		line(out.Agents)
		fmt.Fprintf(w, "Editor isolation: %s\n", out.Editor)
	})
}

var adminInstanceApplicationsListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show Word, Sheet and agents",
	Args:  cobra.NoArgs,
	RunE:  showAdminApplications,
}

var adminInstanceApplicationsEnableCmd = &cobra.Command{
	Use:   "enable <word|sheet|agents>",
	Short: "Turn an application, or agents in Word and Sheet, on",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return setAdminApplication(cmd, args[0], "enabled", nil)
	},
}

var adminInstanceApplicationsDisableCmd = &cobra.Command{
	Use:   "disable <word|sheet|agents>",
	Short: "Turn an application, or agents in Word and Sheet, off",
	Long: `PATCH /api/v1/admin/instance/artifacts/applications/{app}. No document is
deleted or changed. Editors that are already open may finish and save for
--drain (default 15m, at most 24h); --now closes at once. Agents stop at once.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		now, _ := cmd.Flags().GetBool("now")
		drain, _ := cmd.Flags().GetDuration("drain")
		var seconds *int
		if now {
			zero := 0
			seconds = &zero
		} else if cmd.Flags().Changed("drain") {
			if drain < 0 || drain > 24*time.Hour {
				return cli.WithExitCode(fmt.Errorf("--drain must be between 0 and 24h"), cli.ExitValidation)
			}
			s := int(drain / time.Second)
			seconds = &s
		}
		return setAdminApplication(cmd, args[0], "disabled", seconds)
	},
}

func setAdminApplication(cmd *cobra.Command, name, desired string, drainSeconds *int) error {
	subject, err := craftSubject(name, true)
	if err != nil {
		return err
	}
	client, err := requireAuthInstance()
	if err != nil {
		return err
	}
	body := map[string]any{"desired": desired}
	if drainSeconds != nil {
		body["drain_seconds"] = *drainSeconds
	}
	var out adminCraftApplications
	if err := patchJSON(client, craftApplicationsPath+"/"+subject, body, &out); err != nil {
		return err
	}
	return printAdminApplications(cmd, out)
}

var adminInstanceApplicationsDoctorCmd = &cobra.Command{
	Use:   "doctor <word|sheet>",
	Short: "Start an application's document engine and check it answers",
	Long: `POST /api/v1/admin/instance/artifacts/applications/{app}/diagnostics. The
first run after a server start compiles the engine and takes a few seconds.
It reads and writes no document. Exits non-zero when the check fails.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		subject, err := craftSubject(args[0], false)
		if err != nil {
			return err
		}
		client, err := requireAuthInstance()
		if err != nil {
			return err
		}
		var out adminCraftDiagnostics
		if err := postJSON(client, craftApplicationsPath+"/"+subject+"/diagnostics", map[string]any{}, &out); err != nil {
			return err
		}
		if err := resolvedFormatter(cmd).AutoHuman(out, func() {
			w := cmd.OutOrStdout()
			if out.Passed {
				fmt.Fprintf(w, "%s: passed in %d ms (%s, %s)\n", out.ID, out.DurationMS, out.Runtime, out.Version)
			} else {
				fmt.Fprintf(w, "%s: failed: %s\n", out.ID, out.Message)
			}
			fmt.Fprintf(w, "Editor isolation: %s\n", out.Editor)
		}); err != nil {
			return err
		}
		if !out.Passed {
			return cli.WithExitCode(fmt.Errorf("%s self-test failed (%s)", out.ID, out.Reason), cli.ExitGeneric)
		}
		return nil
	},
}

var adminInstanceApplicationsInstallCmd = &cobra.Command{
	Use:   "install <word|sheet>",
	Short: "Download, or install from a file, an application's package",
	Long: `POST /api/v1/admin/instance/artifacts/applications/{app}/install.
Without --file the server downloads the package this Crewship release trusts;
the command waits until it is verified and running (--no-wait returns at
once). With --file it uploads that package (offline), which must be the same
trusted package. Installing does not turn the application on for anyone.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		subject, err := craftSubject(args[0], false)
		if err != nil {
			return err
		}
		client, err := requireAuthInstance()
		if err != nil {
			return err
		}
		file, _ := cmd.Flags().GetString("file")
		noWait, _ := cmd.Flags().GetBool("no-wait")
		timeout, _ := cmd.Flags().GetDuration("timeout")
		path := craftApplicationsPath + "/" + subject + "/install"
		var out adminCraftApplications
		if file != "" {
			f, err := os.Open(file)
			if err != nil {
				return cli.WithExitCode(err, cli.ExitValidation)
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil {
				return err
			}
			upload := client.WithTimeout(timeout)
			req, err := upload.NewRequest(cmd.Context(), http.MethodPost, path, f)
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/octet-stream")
			req.ContentLength = info.Size()
			resp, err := upload.HTTPClient.Do(req)
			if err != nil {
				return &cli.ConnectionError{Err: err}
			}
			if err := cli.CheckError(resp); err != nil {
				return err
			}
			if err := cli.ReadJSON(resp, &out); err != nil {
				return err
			}
			return printAdminApplications(cmd, out)
		}
		if err := postJSON(client, path, nil, &out); err != nil {
			return err
		}
		if !noWait {
			if out, err = waitForInstall(cmd.Context(), client, subject, timeout); err != nil {
				return err
			}
		}
		return printAdminApplications(cmd, out)
	},
}

// waitForInstall polls until the application is no longer installing.
func waitForInstall(ctx context.Context, client *cli.Client, subject string, timeout time.Duration) (adminCraftApplications, error) {
	deadline := time.Now().Add(timeout)
	for {
		var out adminCraftApplications
		if err := getJSON(client, craftApplicationsPath, &out); err != nil {
			return out, err
		}
		for _, a := range out.Applications {
			if a.ID != subject || a.Reason == "component_installing" {
				continue
			}
			if a.Reason == "component_install_failed" {
				detail := a.Message
				if a.Component != nil && a.Component.Detail != "" {
					detail = a.Component.Detail
				}
				return out, cli.WithExitCode(fmt.Errorf("%s install failed: %s", a.Label, detail), cli.ExitGeneric)
			}
			return out, nil
		}
		if time.Now().After(deadline) {
			return out, cli.WithExitCode(fmt.Errorf("%s is still installing after %s; check again with `crewship admin instance applications`", subject, timeout), cli.ExitGeneric)
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func init() {
	adminInstanceApplicationsInstallCmd.Flags().String("file", "", "install this package file (offline) instead of downloading it")
	adminInstanceApplicationsInstallCmd.Flags().Bool("no-wait", false, "return once the download has started")
	adminInstanceApplicationsInstallCmd.Flags().Duration("timeout", 20*time.Minute, "how long to wait for the installation")
	adminInstanceApplicationsDisableCmd.Flags().Duration("drain", 15*time.Minute, "let open editors finish and save for this long")
	adminInstanceApplicationsDisableCmd.Flags().Bool("now", false, "close at once, without a drain")
	adminInstanceApplicationsCmd.AddCommand(adminInstanceApplicationsListCmd, adminInstanceApplicationsEnableCmd,
		adminInstanceApplicationsDisableCmd, adminInstanceApplicationsDoctorCmd, adminInstanceApplicationsInstallCmd)
	adminInstanceCmd.AddCommand(adminInstanceApplicationsCmd)
}
