package main

import (
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/buildinfo"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// flagVersionRemote is `--remote` on `crewship version` (#1645).
//
// Not spelled `--server`: that is a persistent string flag on the root
// command (the target URL), so a boolean of the same name would have
// collided with it. `crewship version --remote --server https://dev1…`
// reads correctly and keeps `--server` meaning the same thing everywhere.
var flagVersionRemote bool

// versionClientInfo is the local half of the report.
type versionClientInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	Dirty     *bool  `json:"dirty"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// versionServerInfo is the remote half, decoded from GET
// /api/v1/system/version plus the URL it was asked of.
type versionServerInfo struct {
	URL           string `json:"url"`
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	BuildTime     string `json:"build_time"`
	Dirty         *bool  `json:"dirty"`
	GoVersion     string `json:"go_version"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	SchemaVersion int    `json:"schema_version"`
}

// versionServerResponse is the wire shape (internal/api/system.go →
// SystemHandler.Version). `current` is the version key: it predates this
// command and drives the web UI's update banner, so it is not renamed.
type versionServerResponse struct {
	Current       string `json:"current"`
	Commit        string `json:"commit"`
	BuildTime     string `json:"build_time"`
	Dirty         *bool  `json:"dirty"`
	GoVersion     string `json:"go_version"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	SchemaVersion int    `json:"schema_version"`
}

type versionPayload struct {
	Client versionClientInfo  `json:"client"`
	Server *versionServerInfo `json:"server,omitempty"`
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Long: "Print the local binary's build identity.\n\n" +
		"With --remote, also ask the configured server which build IT is running —\n" +
		"the only way to tell a stale deployment apart from a stale CLI.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Resolve rather than print the raw ldflags vars: a binary built by
		// dev.sh (`go build`, no -ldflags) has commit "none" and date
		// "unknown" but does carry VCS stamps, which is the only truthful
		// answer available on that path (#1645).
		local := buildinfo.Resolve(version, commit, date)
		payload := versionPayload{Client: versionClientInfo{
			Version:   version,
			Commit:    local.Commit,
			BuildTime: local.BuildTime,
			Dirty:     local.Dirty,
			GoVersion: local.GoVersion,
			OS:        local.OS,
			Arch:      local.Arch,
		}}

		// Without --remote this command touches no network at all. It is the
		// first thing run on a broken install; a dial here would turn a local
		// question into a timeout.
		if flagVersionRemote {
			srv, err := fetchServerVersion()
			if err != nil {
				return err
			}
			payload.Server = srv
		}

		return newFormatter().AutoHuman(payload, func() {
			fmt.Printf("%screwship %s%s\n", cli.Bold, payload.Client.Version, cli.Reset)
			printVersionBlock(payload.Client.Commit, payload.Client.BuildTime,
				payload.Client.Dirty, payload.Client.GoVersion,
				payload.Client.OS, payload.Client.Arch, 0)
			if payload.Server != nil {
				fmt.Println()
				fmt.Printf("%sserver %s%s\n", cli.Bold, payload.Server.URL, cli.Reset)
				fmt.Printf("  version: %s\n", orUnknown(payload.Server.Version))
				printVersionBlock(payload.Server.Commit, payload.Server.BuildTime,
					payload.Server.Dirty, payload.Server.GoVersion, payload.Server.OS,
					payload.Server.Arch, payload.Server.SchemaVersion)
			}
		})
	},
}

// fetchServerVersion asks the configured server what build it is running.
func fetchServerVersion() (*versionServerInfo, error) {
	if err := requireAuth(); err != nil {
		return nil, err
	}
	client := newAPIClient()

	resp, err := client.Get("/api/v1/system/version")
	if err != nil {
		return nil, fmt.Errorf("server version: %w", err)
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var body versionServerResponse
	if err := cli.ReadJSON(resp, &body); err != nil {
		return nil, err
	}

	return &versionServerInfo{
		URL:           cli.ResolveServer(flagServer, cliCfg),
		Version:       body.Current,
		Commit:        body.Commit,
		BuildTime:     body.BuildTime,
		Dirty:         body.Dirty,
		GoVersion:     body.GoVersion,
		OS:            body.OS,
		Arch:          body.Arch,
		SchemaVersion: body.SchemaVersion,
	}, nil
}

// printVersionBlock renders the fields shared by both halves of the report.
// schema is printed only when non-zero — the local binary has no "schema the
// server expects", and an older server omits the key entirely.
func printVersionBlock(commitSHA, built string, dirty *bool, goVersion, goos, goarch string, schema int) {
	fmt.Printf("  commit:  %s%s\n", orUnknown(commitSHA), dirtySuffix(dirty))
	fmt.Printf("  built:   %s\n", orUnknown(built))
	fmt.Printf("  go:      %s\n", orUnknown(goVersion))
	fmt.Printf("  os/arch: %s\n", orUnknown(strings.Trim(goos+"/"+goarch, "/")))
	if schema > 0 {
		// Printed bare, not as "v<n>": past the closed legacy sequential
		// block a migration version is a YYYYMMDDHHMMSS timestamp
		// (internal/database/migrate_registry.go), so "v20260801150210"
		// would read as a release number it is not. As a timestamp it also
		// answers the deploy question directly — the newest schema change
		// this server carries was authored at that moment.
		fmt.Printf("  schema:  %d\n", schema)
	}
}

// dirtySuffix renders the three states of "was the tree dirty" honestly. An
// unstamped build genuinely does not know, and saying nothing is the only
// answer there that does not assert something false.
func dirtySuffix(dirty *bool) string {
	switch {
	case dirty == nil:
		return ""
	case *dirty:
		return " (uncommitted changes)"
	default:
		return " (clean)"
	}
}

// orUnknown keeps a missing field visibly missing. A blank column reads as a
// rendering bug; "unknown" reads as the fact it is.
func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

func init() {
	versionCmd.Flags().BoolVar(&flagVersionRemote, "remote", false,
		"also report the build the configured server is running")
}
