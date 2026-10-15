package pagebuild

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestImageResolver(t *testing.T) {
	newer := "sha256:" + strings.Repeat("b", 64)
	older := "sha256:" + strings.Repeat("a", 64)
	explicit := "sha256:" + strings.Repeat("c", 64)

	t.Run("explicit wins without lookup", func(t *testing.T) {
		r := &ImageResolver{Explicit: explicit, Lookup: func(context.Context) ([]string, error) {
			t.Fatal("lookup must not run when an image is configured")
			return nil, nil
		}}
		if got, err := r.Image(t.Context()); err != nil || got != explicit {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("explicit tag is still rejected", func(t *testing.T) {
		r := &ImageResolver{Explicit: "crewship-pages-build:local"}
		if _, err := r.Image(t.Context()); err == nil {
			t.Fatal("mutable tag accepted")
		}
	})
	t.Run("newest labelled image", func(t *testing.T) {
		r := &ImageResolver{Lookup: func(context.Context) ([]string, error) { return []string{newer, older}, nil }}
		if got, err := r.Image(t.Context()); err != nil || got != newer {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("none is not enabled", func(t *testing.T) {
		r := &ImageResolver{Lookup: func(context.Context) ([]string, error) { return nil, nil }}
		if _, err := r.Image(t.Context()); !errors.Is(err, ErrNotEnabled) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("docker failure is not enabled", func(t *testing.T) {
		r := &ImageResolver{Lookup: func(context.Context) ([]string, error) { return nil, errors.New("no daemon") }}
		if _, err := r.Image(t.Context()); !errors.Is(err, ErrNotEnabled) || !strings.Contains(err.Error(), "no daemon") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("enable is seen after the TTL without restart", func(t *testing.T) {
		now := time.Unix(0, 0)
		var ids []string
		calls := 0
		r := &ImageResolver{
			Lookup: func(context.Context) ([]string, error) { calls++; return ids, nil },
			Now:    func() time.Time { return now },
		}
		if _, err := r.Image(t.Context()); !errors.Is(err, ErrNotEnabled) {
			t.Fatalf("got %v", err)
		}
		ids = []string{newer}
		if _, err := r.Image(t.Context()); !errors.Is(err, ErrNotEnabled) || calls != 1 {
			t.Fatalf("cached result not reused: %v, %d lookups", err, calls)
		}
		now = now.Add(3 * time.Second)
		if got, err := r.Image(t.Context()); err != nil || got != newer {
			t.Fatalf("got %q, %v", got, err)
		}
		ids = nil
		r.Invalidate()
		if _, err := r.Image(t.Context()); !errors.Is(err, ErrNotEnabled) {
			t.Fatalf("invalidate ignored: %v", err)
		}
	})
	t.Run("found image is reused for the TTL", func(t *testing.T) {
		now := time.Unix(0, 0)
		calls := 0
		r := &ImageResolver{Lookup: func(context.Context) ([]string, error) { calls++; return []string{newer}, nil }, Now: func() time.Time { return now }}
		for range 3 {
			now = now.Add(3 * time.Second)
			if _, err := r.Image(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if calls != 1 {
			t.Fatalf("%d lookups within the TTL, want 1", calls)
		}
	})
	t.Run("not enabled is not re-checked per request", func(t *testing.T) {
		calls := 0
		r := &ImageResolver{Lookup: func(context.Context) ([]string, error) { calls++; return nil, nil }}
		for range 50 {
			if _, err := r.Image(t.Context()); !errors.Is(err, ErrNotEnabled) {
				t.Fatalf("got %v", err)
			}
		}
		if calls != 1 {
			t.Fatalf("%d docker lookups for 50 requests, want 1", calls)
		}
	})
	t.Run("a cancelled request does not decide for others", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		r := &ImageResolver{Lookup: func(ctx context.Context) ([]string, error) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return []string{newer}, nil
		}}
		if got, err := r.Image(ctx); err != nil || got != newer {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("a hung daemon is bounded", func(t *testing.T) {
		r := &ImageResolver{LookupTimeout: 20 * time.Millisecond, Lookup: func(ctx context.Context) ([]string, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		start := time.Now()
		if _, err := r.Image(t.Context()); !errors.Is(err, ErrNotEnabled) {
			t.Fatalf("got %v", err)
		}
		if time.Since(start) > 2*time.Second {
			t.Fatal("lookup was not bounded")
		}
	})
}
