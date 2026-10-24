//go:build linux

package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// The actual inherited-FD MOCK child witnesses that it received the grant
// before the guard releases. No native WordCraft, SQL, author or delivery
// authority is claimed by this transport-ordering test.
func TestCollaborationSessionDeliveryGuardSpansPhysicalReply(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "grant-received")
	var held atomic.Bool
	released := make(chan struct{})
	r := newConfiguredPumpTestRig(t, "guard", "", marker, func(h *collaborationSessionPumpHandlers) {
		original := h.Delivery
		h.Delivery = func(ctx context.Context, q protocol.DeliveryRenew) (protocol.DeliveryGrant, error) {
			if !held.Load() {
				t.Error("delivery ran outside guard")
			}
			return original(ctx, q)
		}
		h.DeliveryGuard = func(ctx context.Context, _ protocol.DeliveryRenew) (func(), error) {
			held.Store(true)
			return func() {
				defer close(released)
				defer held.Store(false)
				deadline := time.NewTimer(2 * time.Second)
				defer deadline.Stop()
				tick := time.NewTicker(time.Millisecond)
				defer tick.Stop()
				for {
					raw, err := os.ReadFile(marker)
					if err == nil && string(raw) == "delivery-grant-received" {
						return
					}
					select {
					case <-tick.C:
					case <-ctx.Done():
						t.Error("guard ended before physical reply witness")
						return
					case <-deadline.C:
						t.Error("guard released before child received grant")
						return
					}
				}
			}, nil
		}
	})
	if err := pumpWait(t, r.ctx, r.opened); err != nil {
		t.Fatal(err)
	}
	pumpWait(t, r.ctx, r.entered)
	pumpWait(t, r.ctx, released)
	if held.Load() {
		t.Fatal("guard did not release")
	}
	for pumpWait(t, r.ctx, r.renewed) != "delivery-roundtrip" {
	}
	if r.other.Load() != 0 {
		t.Fatal("mutation/status overtook withheld A")
	}
	close(r.release)
	for pumpWait(t, r.ctx, r.renewed) != "replies-ordered" {
	}
}
func TestCollaborationSessionDeliveryGuardFailsClosedAndReleases(t *testing.T) {
	for _, mode := range []string{"nil-release", "error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			var called, released atomic.Bool
			r := newConfiguredPumpTestRig(t, "normal", "", "", func(h *collaborationSessionPumpHandlers) {
				h.DeliveryGuard = func(context.Context, protocol.DeliveryRenew) (func(), error) {
					if mode == "nil-release" {
						return nil, nil
					}
					release := func() { released.Store(true) }
					if mode == "error" {
						return release, errors.New("mock guard unavailable")
					}
					return release, nil
				}
				h.Delivery = func(context.Context, protocol.DeliveryRenew) (protocol.DeliveryGrant, error) {
					called.Store(true)
					panic("mock guarded callback panic")
				}
			})
			select {
			case <-r.runtime.Done():
			case <-r.ctx.Done():
				t.Fatal("invalid guard left runtime alive")
			}
			// The process watcher may report death before the pump's defers finish.
			// Wait on its own completion to inspect release and callback state.
			select {
			case <-r.done:
				r.done <- errCollaborationSessionPump
			case <-r.ctx.Done():
				t.Fatal("pump guard did not finish")
			}
			if called.Load() != (mode == "panic") || released.Load() != (mode != "nil-release") || r.other.Load() != 0 {
				t.Fatal("guard failure admitted work or leaked hold", called.Load(), released.Load(), r.other.Load())
			}
		})
	}
}
