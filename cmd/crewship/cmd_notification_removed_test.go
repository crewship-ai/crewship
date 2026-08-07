package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// `crewship notification` drove five endpoints over a table nothing wrote to
// (#1751). Its help text even had the relationship backwards — it called
// notifications "the entity-scoped event log feeding [the inbox]" when nothing
// fed anything. Command group and routes are both gone.
//
// This guard is the CLI third of the removal, alongside
// TestNotificationsSurfaceStaysRemoved (internal/api) and
// TestDropDeadNotifications_* (internal/database). Re-adding the command
// without its server side fails here; re-adding the routes without the table
// fails there.
func TestNotificationCommandStaysRemoved(t *testing.T) {
	var found []string
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			for _, n := range append([]string{sub.Name()}, sub.Aliases...) {
				if n == "notification" || n == "notifications" || n == "notif" {
					found = append(found, c.Name()+" "+sub.Name())
					break
				}
			}
			walk(sub)
		}
	}
	walk(rootCmd)

	if len(found) > 0 {
		t.Errorf("commands %v exist — `crewship notification` was removed in #1751 because "+
			"every endpoint behind it read a table with no writer. The human-attention surface "+
			"is `crewship inbox`; the outbound channels are `crewship notifychannel`.", found)
	}
}

// The live surfaces that kept the word must still be reachable, so the guard
// above cannot be satisfied by deleting too much.
func TestInboxAndNotifyChannelCommandsSurvive(t *testing.T) {
	have := map[string]bool{}
	for _, c := range rootCmd.Commands() {
		have[c.Name()] = true
	}
	for _, want := range []string{"inbox", "notifychannel"} {
		if !have[want] {
			t.Errorf("command %q is missing — #1751 removed the entity-scoped feed only", want)
		}
	}
}

// The inbox help pointed at `crewship notification` as its lower-level
// counterpart. A dangling pointer in help text is how a removed surface keeps
// being looked for.
func TestInboxHelpDoesNotPointAtRemovedNotificationCommand(t *testing.T) {
	var inbox *cobra.Command
	for _, c := range rootCmd.Commands() {
		if c.Name() == "inbox" {
			inbox = c
			break
		}
	}
	if inbox == nil {
		t.Fatal("no `inbox` command")
	}
	if strings.Contains(inbox.Long, "crewship notification") {
		t.Error("`crewship inbox --help` still refers the reader to `crewship notification`, " +
			"which no longer exists")
	}
}
