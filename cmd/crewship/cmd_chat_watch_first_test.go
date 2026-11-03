//go:build !clionly

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Codex #4685 round 3: a connection that fails before it ever subscribes is not
// the first subscription. The first successful subscribe must still report
// first=true, so the initial room read runs as the strict first read (a 404/403
// fails the command) instead of a reconnect catch-up that only logs it.
func TestWatchChannelFirstSurvivesFailureBeforeSubscribe(t *testing.T) {
	savedOnce, savedBackoff := watchOnce, watchReconnectBackoff
	defer func() { watchOnce, watchReconnectBackoff = savedOnce, savedBackoff }()
	watchReconnectBackoff = time.Millisecond
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchOnce = func(ctx context.Context, _ *cli.Client, _ string, _ func(*cli.WSMessage), onSubscribed func()) error {
		calls++
		if calls == 1 {
			return errors.New("dial: connection refused")
		}
		onSubscribed()
		cancel()
		return nil
	}
	var firsts []bool
	if err := watchChannelSubscribed(ctx, nil, "room", nil, func(first bool) { firsts = append(firsts, first) }); err != nil {
		t.Fatal(err)
	}
	if len(firsts) != 1 || !firsts[0] {
		t.Fatalf("subscribed hooks %v, want [true] for the first successful subscribe", firsts)
	}
}
