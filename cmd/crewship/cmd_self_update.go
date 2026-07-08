package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/crewship-ai/crewship/internal/update"
	"github.com/spf13/cobra"
)

var selfUpdateCheckOnly bool

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
}

func runSelfUpdate(ctx context.Context, checkOnly bool) error {
	if version == "dev" || version == "" {
		return fmt.Errorf("this is a development build (version %q) — self-update only works on released binaries", version)
	}

	res, err := update.Check(ctx, version)
	if err != nil {
		return fmt.Errorf("checking for updates: %w", err)
	}
	if res == nil {
		fmt.Println("update check skipped (dev build or CREWSHIP_SKIP_UPDATE_CHECK=1)")
		return nil
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

	switch update.DetectChannel(exePath, dirWritable(binDir)) {
	case update.ChannelHomebrew:
		fmt.Println("\nInstalled via Homebrew — upgrading with 'brew upgrade crewship'…")
		brew := exec.CommandContext(ctx, "brew", "upgrade", "crewship")
		brew.Stdout, brew.Stderr, brew.Stdin = os.Stdout, os.Stderr, os.Stdin
		if err := brew.Run(); err != nil {
			return fmt.Errorf("brew upgrade crewship failed: %w", err)
		}
		return nil

	case update.ChannelPackaged:
		return fmt.Errorf("%s", update.PackagedChannelGuidance(exePath))

	default: // ChannelInstaller
		fmt.Printf("\nDownloading %s and swapping %s…\n", res.Latest, exePath)
		result, err := update.ApplyInstallerUpdate(ctx, res.Latest, binDir, version)
		if err != nil {
			return fmt.Errorf("self-update failed (binary unchanged): %w", err)
		}
		// Sanity-check the freshly swapped binary actually runs.
		out, verr := exec.CommandContext(ctx, exePath, "version").CombinedOutput()
		if verr != nil {
			return fmt.Errorf(
				"updated binary failed its version sanity check: %w\n%s\n"+
					"Reinstall with the official installer if crewship no longer runs:\n"+
					"  curl -fsSL https://raw.githubusercontent.com/crewship-ai/crewship/main/scripts/install.sh | bash",
				verr, out)
		}
		fmt.Printf("Updated crewship %s → %s (replaced: %v)\n", result.FromVersion, result.ToVersion, result.Replaced)
		fmt.Println("Migrations (if any) run on the next 'crewship start'; a pre-migration snapshot is taken automatically.")
		return nil
	}
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
