package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/update"
	"github.com/spf13/cobra"
)

var (
	selfUpdateCheckOnly  bool
	selfUpdateSystemd    bool
	selfUpdateService    string
	selfUpdateHealthWait time.Duration
	selfUpdateHealthURL  string
	selfUpdatePort       int
)

var selfUpdateCmd = &cobra.Command{
	Use:   "self-update",
	Short: "Upgrade the crewship binary to the latest release",
	Long: `Upgrade this crewship binary to the latest published release.

How it upgrades depends on how crewship was installed:

  • Homebrew        → runs 'brew upgrade crewship'
  • installer/tarball → downloads the matching release asset, verifies its
                        checksum, and atomically swaps the binary in place
                        (plus the bundled sidecar + entrypoint.sh when present)
  • package manager  → refuses and prints the command to run instead

Use --check to see whether a newer release exists without changing anything.

For a long-running install managed by systemd (a VM or bare-metal server),
add --systemd: crewship stops the service, swaps the binary, starts it again,
and health-checks the new server — automatically rolling back to the previous
binary if it fails to start or comes up unhealthy.

Database migrations run on the NEXT 'crewship start'; a pre-migration
snapshot is taken automatically, so a bad upgrade is a one-step rollback
(reinstall the old version + restore the snapshot). See the upgrades guide.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSelfUpdate(cmd.Context(), selfUpdateCheckOnly)
	},
}

func init() {
	selfUpdateCmd.Flags().BoolVar(&selfUpdateCheckOnly, "check", false,
		"only report whether a newer release exists; make no changes")
	// --systemd, not --server: the root command already owns a persistent
	// -s/--server flag (the crewship server URL), which self-update does not
	// use (it talks to GitHub + the local service manager). Reusing the name
	// would shadow that global flag on this command.
	selfUpdateCmd.Flags().BoolVar(&selfUpdateSystemd, "systemd", false,
		"orchestrate a systemd-managed upgrade: stop → swap → start → health-check, with auto-rollback")
	selfUpdateCmd.Flags().StringVar(&selfUpdateService, "service", "crewship",
		"systemd unit to orchestrate with --systemd")
	selfUpdateCmd.Flags().DurationVar(&selfUpdateHealthWait, "health-timeout", 60*time.Second,
		"how long --systemd waits for the new server to become healthy before rolling back")
	selfUpdateCmd.Flags().StringVar(&selfUpdateHealthURL, "health-url", "",
		"full health-check URL for --systemd (overrides --port; default http://127.0.0.1:<port>/healthz)")
	selfUpdateCmd.Flags().IntVar(&selfUpdatePort, "port", 0,
		"server port to health-check with --systemd (default: the unit's env via systemctl, else config, else 8080)")
}

func runSelfUpdate(ctx context.Context, checkOnly bool) error {
	if version == "dev" || version == "" {
		return fmt.Errorf("this is a development build (version %q) — self-update only works on released binaries", version)
	}

	// Explicit command → CheckExplicit: always fresh (no 24h cache) and it
	// ignores CREWSHIP_SKIP_UPDATE_CHECK (that env only mutes the passive
	// boot banner, not a self-update the user typed on purpose).
	res, err := update.CheckExplicit(ctx, version)
	if err != nil {
		return fmt.Errorf("checking for updates: %w", err)
	}
	if res == nil {
		return fmt.Errorf("cannot self-update a development build (version %q)", version)
	}

	if !res.Newer {
		fmt.Printf("crewship %s is already the latest release.\n", version)
		return nil
	}
	fmt.Printf("A newer release is available: %s → %s\n", res.Current, res.Latest)
	if res.URL != "" {
		fmt.Printf("  release notes: %s\n", res.URL)
	}
	if checkOnly {
		fmt.Println("\nRun 'crewship self-update' to install it.")
		return nil
	}

	// Resolve the real binary path (follow symlinks so a Homebrew
	// prefix/bin symlink resolves to its Cellar target).
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating current binary: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exePath); rerr == nil {
		exePath = resolved
	}
	binDir := filepath.Dir(exePath)

	channel := update.DetectChannel(exePath, dirWritable(binDir))

	// --systemd orchestrates a self-installed systemd binary. Homebrew
	// and package-manager installs manage their own service lifecycle, so
	// refuse there and point at the right tool rather than fighting it.
	if selfUpdateSystemd && channel != update.ChannelInstaller {
		switch channel {
		case update.ChannelHomebrew:
			return fmt.Errorf("--systemd is for a self-managed systemd install; this is a Homebrew install — " +
				"run 'brew upgrade' and restart the service via brew services / your unit")
		default:
			return fmt.Errorf("--systemd is for a self-managed systemd install; this binary is package-managed — " +
				"upgrade via apt/dnf (the package restarts the service for you)")
		}
	}

	switch channel {
	case update.ChannelHomebrew:
		// Read the formula from the Cellar path so a crewship-cli install
		// upgrades crewship-cli, not the wrong crewship formula.
		formula := update.FormulaFromPath(exePath, cliOnlyVariant)
		fmt.Printf("\nInstalled via Homebrew — upgrading with 'brew upgrade %s'…\n", formula)
		brew := exec.CommandContext(ctx, "brew", "upgrade", formula)
		brew.Stdout, brew.Stderr, brew.Stdin = os.Stdout, os.Stderr, os.Stdin
		if err := brew.Run(); err != nil {
			return fmt.Errorf("brew upgrade %s failed: %w", formula, err)
		}
		return nil

	case update.ChannelPackaged:
		return fmt.Errorf("%s", update.PackagedChannelGuidance(exePath))

	default: // ChannelInstaller
		if selfUpdateSystemd {
			return runServerSelfUpdate(ctx, res.Latest, exePath)
		}
		fmt.Printf("\nDownloading %s and swapping %s…\n", res.Latest, exePath)
		result, err := update.ApplyInstallerUpdate(ctx, res.Latest, exePath, cliOnlyVariant, version)
		if err != nil {
			return fmt.Errorf("self-update failed (binary unchanged): %w", err)
		}
		// Sanity-check the freshly swapped binary actually runs; on failure
		// roll back every swapped file (binary + companions) so a bad swap
		// never leaves crewship broken or in a mixed state. Bound the probe
		// so a hung bad build fails fast into the rollback rather than
		// stalling self-update indefinitely.
		sanityCtx, sanityCancel := context.WithTimeout(ctx, 30*time.Second)
		out, verr := exec.CommandContext(sanityCtx, exePath, "version").CombinedOutput()
		sanityCancel()
		if verr != nil {
			restoreErr := update.RestoreBackups(result.Replaced)
			if restoreErr != nil {
				return fmt.Errorf(
					"updated binary failed its version sanity check: %w\n%s\n"+
						"AND rollback failed: %v — restore manually from %s, or reinstall:\n"+
						"  curl -fsSL https://raw.githubusercontent.com/crewship-ai/crewship/main/scripts/install.sh | bash",
					verr, out, restoreErr, result.BackupPath)
			}
			return fmt.Errorf(
				"updated binary failed its version sanity check: %w\n%s\n"+
					"rolled back to the previous version (%s) — no change applied",
				verr, out, result.FromVersion)
		}
		fmt.Printf("Updated crewship %s → %s (replaced: %v)\n", result.FromVersion, result.ToVersion, result.Replaced)
		fmt.Printf("Previous binary kept at %s for rollback.\n", result.BackupPath)
		fmt.Println("Migrations (if any) run on the next 'crewship start'; a pre-migration snapshot is taken automatically.")
		return nil
	}
}

// runServerSelfUpdate orchestrates a systemd-managed in-place upgrade: stop →
// swap → start → health-check, auto-rolling-back to the previous binary if the
// new one fails. The stop/swap/start/health/rollback state machine lives in
// internal/update (RunServerUpdate) and is unit-tested with a mock service
// manager; here we just wire the production collaborators.
func runServerSelfUpdate(ctx context.Context, latestTag, exePath string) error {
	svc, err := update.NewSystemdService(selfUpdateService)
	if err != nil {
		return err
	}

	healthURL := resolveHealthURL(ctx, svc)
	fmt.Printf("\nServer upgrade of unit %q: stop → swap → start → health-check %s (up to %s)…\n",
		selfUpdateService, healthURL, selfUpdateHealthWait)

	out, uerr := update.RunServerUpdate(ctx, update.ServerUpdateDeps{
		Manager: svc,
		// Pre-fetch + verify the release BEFORE the service is stopped, so a
		// download/checksum failure never causes downtime.
		Prepare: func(ctx context.Context) (any, error) {
			return update.PrepareInstallerUpdate(ctx, latestTag, exePath, cliOnlyVariant, version)
		},
		Commit: func(_ context.Context, prepared any) (*update.SelfUpdateResult, error) {
			p, ok := prepared.(*update.PreparedUpdate)
			if !ok {
				return nil, fmt.Errorf("internal error: unexpected prepared update type %T", prepared)
			}
			return p.Commit()
		},
		Health:   update.HTTPHealthChecker(healthURL, selfUpdateHealthWait, time.Second),
		Rollback: update.RestoreBackups,
		Log:      func(m string) { fmt.Println("  " + m) },
	})
	if uerr != nil {
		// out.RolledBack is already reflected in uerr's message (which names the
		// rollback + any restore-snapshot guidance). Exit non-zero either way so
		// automation notices the upgrade didn't take.
		return fmt.Errorf("server upgrade failed: %w", uerr)
	}

	fmt.Printf("Upgraded crewship %s → %s and restarted unit %q (healthy).\n",
		out.Result.FromVersion, out.Result.ToVersion, selfUpdateService)
	fmt.Printf("Previous binary kept at %s for rollback.\n", out.Result.BackupPath)
	return nil
}

// resolveHealthURL picks the URL the health probe hits, in priority order:
//   - --health-url (explicit override),
//   - http://127.0.0.1:<port>/healthz where <port> is --port, else the port the
//     RUNNING unit uses (read from systemd via `systemctl show`, which survives
//     `sudo` stripping our env), else the server's own config (YAML + surviving
//     env, via config.Load), else 8080.
//
// Reading the port from the unit matters because `sudo crewship self-update`
// runs with a scrubbed environment, so CREWSHIP_PORT set for the service is not
// visible to us — asking systemd (and then the config file) is how we learn the
// port the server actually listens on.
func resolveHealthURL(ctx context.Context, svc *update.SystemdService) string {
	if selfUpdateHealthURL != "" {
		return selfUpdateHealthURL
	}
	port := selfUpdatePort
	if port == 0 {
		// Sudo-proof source #1: the running unit's resolved environment, which
		// carries CREWSHIP_PORT even though sudo scrubbed it from ours.
		port = svc.UnitEnvPort(ctx)
	}
	if port == 0 {
		// Source #2: the config the server itself reads (YAML file + any
		// surviving CREWSHIP_PORT env), exactly as `crewship start` resolves
		// the port. Best-effort.
		port = configServerPort()
	}
	if port == 0 {
		port = 8080
	}
	return fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
}

// configServerPort resolves the port the way `crewship start` does — via
// config.Load (YAML config file + CREWSHIP_PORT env) — so --systemd probes the
// right port when the operator set it in the config rather than the unit env.
// Best-effort: any load failure yields 0 and the caller falls back. Sidecar
// autodetect is irrelevant to a port read, so it's skipped so the load can't
// error on a host without the sidecar staged.
func configServerPort() int {
	prev, had := os.LookupEnv("CREWSHIP_SKIP_SIDECAR")
	_ = os.Setenv("CREWSHIP_SKIP_SIDECAR", "1")
	defer func() {
		if had {
			_ = os.Setenv("CREWSHIP_SKIP_SIDECAR", prev)
		} else {
			_ = os.Unsetenv("CREWSHIP_SKIP_SIDECAR")
		}
	}()
	cfg, err := config.Load("")
	if err != nil || cfg == nil {
		return 0
	}
	return cfg.Server.Port
}

// dirWritable reports whether this process can create files in dir — the
// signal (with the path shape) that separates an installer install we may
// overwrite from a package-managed / read-only one we must not.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".crewship-write-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}
