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
		now = now.Add(11 * time.Second)
		if got, err := r.Image(t.Context()); err != nil || got != newer {
			t.Fatalf("got %q, %v", got, err)
		}
		ids = nil
		r.Invalidate()
		if _, err := r.Image(t.Context()); !errors.Is(err, ErrNotEnabled) {
			t.Fatalf("invalidate ignored: %v", err)
		}
	})
}
