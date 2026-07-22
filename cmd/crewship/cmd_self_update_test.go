package main

// Tests for the self-update channel dispatch: which arm each install channel
// routes to, that the three "we don't own this binary" channels refuse the
// in-place swap outright, and that --systemd is rejected everywhere except a
// self-managed installer install.
//
// The network/disk halves are stubbed through the brewUpgrade and
// installerSelfUpdate seams — a routing test must never download a release or
// touch the running binary.

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/update"
)

// stubSelfUpdateSeams replaces both dispatch seams with recorders and restores
// them at test end. The returned pointers report which arm ran.
// Stubbing serverSelfUpdate is not optional: unstubbed, this test would stop
// the real `crewship` systemd unit on any Linux host that has one.
func stubSelfUpdateSeams(t *testing.T, brewErr, installerErr error) (brewCalls, installerCalls, systemdCalls *[]string) {
	t.Helper()
	origBrew, origInstaller, origServer := brewUpgrade, installerSelfUpdate, serverSelfUpdate
	t.Cleanup(func() {
		brewUpgrade, installerSelfUpdate, serverSelfUpdate = origBrew, origInstaller, origServer
	})

	brews := []string{}
	installs := []string{}
	servers := []string{}
	brewUpgrade = func(_ context.Context, formula string) error {
		brews = append(brews, formula)
		return brewErr
	}
	installerSelfUpdate = func(_ context.Context, latestTag, exePath string) error {
		installs = append(installs, latestTag+" "+exePath)
		return installerErr
	}
	serverSelfUpdate = func(_ context.Context, latestTag, exePath string) error {
		servers = append(servers, latestTag+" "+exePath)
		return nil
	}
	return &brews, &installs, &servers
}

func TestSelfUpdateDispatch_RoutesByChannel(t *testing.T) {
	cases := []struct {
		name     string
		channel  update.Channel
		execPath string
		wantErr  bool
		// wantIn is a substring the refusal must carry so the user knows what
		// to run instead.
		wantIn       string
		wantBrew     string // formula the homebrew arm must upgrade ("" = no brew call)
		wantInstalls int    // times the in-place swap ran
	}{
		{
			name:     "homebrew delegates to brew upgrade",
			channel:  update.ChannelHomebrew,
			execPath: "/opt/homebrew/Cellar/crewship/0.1.0/bin/crewship",
			wantBrew: "crewship",
		},
		{
			// The formula is read from the Cellar segment, so a crewship-cli
			// install upgrades crewship-cli — not the wrong formula.
			name:     "homebrew reads the formula from the Cellar segment",
			channel:  update.ChannelHomebrew,
			execPath: "/usr/local/Cellar/crewship-cli/0.1.0/bin/crewship",
			wantBrew: "crewship-cli",
		},
		{
			name:         "installer swaps in place",
			channel:      update.ChannelInstaller,
			execPath:     "/home/u/.local/bin/crewship",
			wantInstalls: 1,
		},
		{
			name:     "packaged refuses and names the package manager",
			channel:  update.ChannelPackaged,
			execPath: "/usr/bin/crewship",
			wantErr:  true,
			wantIn:   "apt-get",
		},
		{
			name:     "npm refuses and names npm",
			channel:  update.ChannelNPM,
			execPath: "/usr/local/lib/node_modules/@crewship/cli-linux-x64/bin/crewship",
			wantErr:  true,
			wantIn:   "npm i -g crewship@latest",
		},
		{
			name:     "npx says there is nothing to update",
			channel:  update.ChannelNPM,
			execPath: "/home/u/.npm/_npx/abc123/node_modules/@crewship/cli-darwin-arm64/bin/crewship",
			wantErr:  true,
			wantIn:   "nothing to update",
		},
		{
			name:     "go install refuses and names go install",
			channel:  update.ChannelGoInstall,
			execPath: "/home/u/go/bin/crewship",
			wantErr:  true,
			wantIn:   "go install github.com/crewship-ai/crewship/cmd/crewship@latest",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			brews, installs, _ := stubSelfUpdateSeams(t, nil, nil)

			_, err := captureStdoutCovCli10(t, func() error {
				return dispatchSelfUpdate(context.Background(), tc.channel, tc.execPath, "v9.9.9", false)
			})

			if tc.wantErr && err == nil {
				t.Fatalf("channel %v: expected a refusal, got nil", tc.channel)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("channel %v: unexpected error: %v", tc.channel, err)
			}
			if tc.wantIn != "" && !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("refusal missing %q: %v", tc.wantIn, err)
			}
			if tc.wantBrew != "" {
				if len(*brews) != 1 || (*brews)[0] != tc.wantBrew {
					t.Errorf("brew upgrade calls = %v, want [%s]", *brews, tc.wantBrew)
				}
			} else if len(*brews) != 0 {
				t.Errorf("brew must not run for channel %v: %v", tc.channel, *brews)
			}
			if len(*installs) != tc.wantInstalls {
				t.Errorf("in-place swap ran %d time(s), want %d (channel %v)", len(*installs), tc.wantInstalls, tc.channel)
			}
		})
	}
}

// TestSelfUpdateDispatch_NonInstallerNeverSwaps is the data-loss guard stated
// once, positively: every channel whose binary we do NOT own must leave it
// alone. ChannelGoInstall is the case that regressed silently before it
// existed — GOBIN is writable, so the swap used to just happen.
func TestSelfUpdateDispatch_NonInstallerNeverSwaps(t *testing.T) {
	for _, ch := range []update.Channel{update.ChannelPackaged, update.ChannelNPM, update.ChannelGoInstall} {
		t.Run(ch.String(), func(t *testing.T) {
			_, installs, _ := stubSelfUpdateSeams(t, nil, nil)
			err := dispatchSelfUpdate(context.Background(), ch, "/somewhere/crewship", "v9.9.9", false)
			if err == nil {
				t.Fatalf("channel %v must refuse to self-update", ch)
			}
			if len(*installs) != 0 {
				t.Errorf("channel %v must not swap the binary, but did: %v", ch, *installs)
			}
			// The refusal has to be actionable, not just a "no".
			if !strings.Contains(err.Error(), "/somewhere/crewship") {
				t.Errorf("refusal should name the binary: %v", err)
			}
		})
	}
}

// TestSelfUpdateDispatch_InstallerSystemd pins that --systemd on an installer
// install takes the orchestrated stop→swap→start path rather than the plain
// in-place swap.
func TestSelfUpdateDispatch_InstallerSystemd(t *testing.T) {
	_, installs, servers := stubSelfUpdateSeams(t, nil, nil)
	if err := dispatchSelfUpdate(context.Background(), update.ChannelInstaller, "/home/u/.local/bin/crewship", "v9.9.9", true); err != nil {
		t.Fatalf("installer + --systemd: %v", err)
	}
	if len(*installs) != 0 {
		t.Errorf("--systemd must not take the plain in-place swap arm: %v", *installs)
	}
	if len(*servers) != 1 {
		t.Errorf("--systemd should orchestrate the service upgrade, calls = %v", *servers)
	}
}

func TestSelfUpdateSystemdGuard(t *testing.T) {
	cases := []struct {
		name    string
		systemd bool
		channel update.Channel
		wantIn  string // "" = must be allowed
	}{
		{name: "no --systemd is always allowed", systemd: false, channel: update.ChannelPackaged},
		{name: "installer is the supported --systemd channel", systemd: true, channel: update.ChannelInstaller},
		{name: "homebrew", systemd: true, channel: update.ChannelHomebrew, wantIn: "brew upgrade"},
		{name: "packaged", systemd: true, channel: update.ChannelPackaged, wantIn: "apt/dnf"},
		// The packaged default arm's "upgrade via apt/dnf" would be plain
		// wrong advice for these two, so each gets its own message.
		{name: "npm", systemd: true, channel: update.ChannelNPM, wantIn: "npm i -g crewship@latest"},
		{name: "go install", systemd: true, channel: update.ChannelGoInstall, wantIn: "go install"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := systemdChannelGuard(tc.systemd, tc.channel)
			if tc.wantIn == "" {
				if err != nil {
					t.Fatalf("channel %v should be allowed with --systemd=%v: %v", tc.channel, tc.systemd, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("--systemd must be rejected for channel %v", tc.channel)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("guard for %v missing %q: %v", tc.channel, tc.wantIn, err)
			}
			if strings.Contains(err.Error(), "apt/dnf") && tc.channel != update.ChannelPackaged {
				t.Errorf("channel %v must not be told to use apt/dnf: %v", tc.channel, err)
			}
		})
	}
}

// TestSelfUpdateLongHelpListsChannels keeps the documented channel list in
// sync with the ones the dispatch actually handles — the help text is where
// users learn why self-update refused.
func TestSelfUpdateLongHelpListsChannels(t *testing.T) {
	for _, want := range []string{"Homebrew", "installer", "package manager", "npm", "go install"} {
		if !strings.Contains(selfUpdateCmd.Long, want) {
			t.Errorf("self-update --help should enumerate the %q channel:\n%s", want, selfUpdateCmd.Long)
		}
	}
}
