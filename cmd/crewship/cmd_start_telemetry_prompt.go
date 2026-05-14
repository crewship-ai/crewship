package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/crewship-ai/crewship/internal/crashreport"
	"github.com/crewship-ai/crewship/internal/database"
	"golang.org/x/term"
)

// maybePromptTelemetry shows a one-time consent prompt at first server start.
// Skips silently in three cases so we never block boot:
//
//   - The operator has already answered (Status returns asked=true).
//   - stdin or stdout is not a TTY (CI, Docker without -it, systemd unit).
//   - CREWSHIP_TELEMETRY_NO_PROMPT=1 — escape hatch for ops scripts that
//     want to seed the value via `crewship telemetry on/off` instead.
//
// Default if the user just presses enter or Ctrl+C: opt-OUT. We always favor
// privacy on the silent path.
func maybePromptTelemetry(ctx context.Context, db *database.DB, logger *slog.Logger) error {
	_, asked, _, err := crashreport.Status(ctx, db.DB)
	if err != nil {
		return fmt.Errorf("read telemetry status: %w", err)
	}
	if asked {
		return nil
	}
	if os.Getenv("CREWSHIP_TELEMETRY_NO_PROMPT") == "1" {
		// Record as "asked, declined" so subsequent boots don't re-prompt.
		_, _, err := crashreport.SetOptIn(ctx, db.DB, false)
		return err
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		// Non-interactive boot. Don't write a setting — we want a real
		// admin to make the call later via `crewship telemetry on/off`.
		// Until they do, Init() treats absent setting as opt-out, so we
		// stay private.
		logger.Info("telemetry prompt skipped (non-interactive); operator can configure later via `crewship telemetry on`")
		return nil
	}

	var optIn bool
	err = huh.NewConfirm().
		Title("Send anonymous crash reports to help improve Crewship?").
		Description(
			"What's sent: Go stack traces, version, OS/arch, anonymous install ID.\n"+
				"What's NEVER sent: workspace data, credentials, request bodies, env vars.\n\n"+
				"You can change your mind any time with `crewship telemetry on/off`.",
		).
		Affirmative("Yes, enable").
		Negative("No, decline").
		Value(&optIn).
		Run()
	if err != nil {
		// User aborted (Ctrl+C) or terminal trouble. Record opt-OUT so we
		// don't loop the prompt every boot.
		logger.Info("telemetry prompt aborted; recording opt-out", "error", err)
		_, _, setErr := crashreport.SetOptIn(ctx, db.DB, false)
		return setErr
	}
	_, installID, err := crashreport.SetOptIn(ctx, db.DB, optIn)
	if err != nil {
		return err
	}
	if optIn {
		fmt.Fprintf(os.Stderr, "Telemetry enabled. Install ID: %s\n", installID)
	} else {
		fmt.Fprintln(os.Stderr, "Telemetry disabled.")
	}
	return nil
}
