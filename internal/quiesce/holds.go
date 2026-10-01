package quiesce

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Hold keys (instance_holds.key).
const (
	HoldRoutines = "routines"
	HoldWebhooks = "webhooks"
	HoldQueue    = "queue"
	// HoldAll holds every automation at once.
	HoldAll = "all"
)

// ErrUnknownHold is returned by Resume for a key that is not held.
var ErrUnknownHold = errors.New("quiesce: that automation is not held")

// ValidHoldKey reports whether key is one of the hold keys.
func ValidHoldKey(key string) bool {
	switch key {
	case HoldRoutines, HoldWebhooks, HoldQueue, HoldAll:
		return true
	}
	return false
}

// Hold is one row of instance_holds.
type Hold struct {
	Key       string    `json:"key"`
	Reason    string    `json:"reason"`
	Count     int       `json:"count"`
	Detail    string    `json:"detail"`
	CreatedAt time.Time `json:"created_at"`
}

// Holds is the in-memory copy of instance_holds the hooks read on every tick
// and request. It is loaded at boot and updated by Resume; nothing else writes
// holds while the server runs (only `crewship recover`, offline, sets them).
type Holds struct {
	mu sync.RWMutex
	m  map[string]Hold
}

// NewHolds returns an empty set.
func NewHolds() *Holds { return &Holds{m: map[string]Hold{}} }

var defaultHolds = NewHolds()

// DefaultHolds is the process-wide set every hook consults.
func DefaultHolds() *Holds { return defaultHolds }

// Held reports whether key (or "all") is held.
func (h *Holds) Held(key string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if _, ok := h.m[HoldAll]; ok {
		return true
	}
	_, ok := h.m[key]
	return ok
}

// List returns the holds ordered by key.
func (h *Holds) List() []Hold {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]Hold, 0, len(h.m))
	for _, v := range h.m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Replace swaps the whole set (tests, and Load).
func (h *Holds) Replace(holds []Hold) {
	m := make(map[string]Hold, len(holds))
	for _, x := range holds {
		m[x.Key] = x
	}
	h.mu.Lock()
	h.m = m
	h.mu.Unlock()
}

// Load reads instance_holds into h. A database without the table (older
// schema, tests) loads as nothing held.
func (h *Holds) Load(ctx context.Context, db *sql.DB) error {
	holds, err := ReadHolds(ctx, db)
	if err != nil {
		return err
	}
	h.Replace(holds)
	return nil
}

// Resume clears key: the row is deleted and the in-memory copy updated. An
// unheld key is ErrUnknownHold. Resuming "all" clears every hold.
func (h *Holds) Resume(ctx context.Context, ex Execer, key string) (Hold, error) {
	h.mu.RLock()
	prev, ok := h.m[key]
	h.mu.RUnlock()
	if !ok {
		return Hold{}, ErrUnknownHold
	}
	q := `DELETE FROM instance_holds WHERE key = ?`
	args := []any{key}
	if key == HoldAll {
		q, args = `DELETE FROM instance_holds`, nil
	}
	if _, err := ex.ExecContext(ctx, q, args...); err != nil {
		return Hold{}, fmt.Errorf("quiesce: resume %s: %w", key, err)
	}
	return prev, nil
}

// Forget drops key from memory after the row's deletion committed.
func (h *Holds) Forget(key string) {
	h.mu.Lock()
	if key == HoldAll {
		h.m = map[string]Hold{}
	} else {
		delete(h.m, key)
	}
	h.mu.Unlock()
}

// Execer is a *sql.DB or *sql.Tx.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Querier is a *sql.DB or *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// ReadHolds reads every row of instance_holds. A missing table reads as none.
func ReadHolds(ctx context.Context, db Querier) ([]Hold, error) {
	rows, err := db.QueryContext(ctx, `SELECT key, COALESCE(reason,''), COALESCE(count,0), COALESCE(detail,''), created_at FROM instance_holds ORDER BY key`)
	if err != nil {
		if isNoSuchTable(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("quiesce: read holds: %w", err)
	}
	defer rows.Close()
	var out []Hold
	for rows.Next() {
		var x Hold
		var created string
		if err := rows.Scan(&x.Key, &x.Reason, &x.Count, &x.Detail, &created); err != nil {
			return nil, fmt.Errorf("quiesce: scan hold: %w", err)
		}
		x.CreatedAt = parseTime(created)
		out = append(out, x)
	}
	return out, rows.Err()
}

// SetHolds writes holds (insert or replace by key).
func SetHolds(ctx context.Context, ex Execer, holds []Hold) error {
	for _, x := range holds {
		if !ValidHoldKey(x.Key) {
			return fmt.Errorf("quiesce: %q is not a hold key", x.Key)
		}
		created := x.CreatedAt
		if created.IsZero() {
			created = time.Now()
		}
		if _, err := ex.ExecContext(ctx, `
INSERT INTO instance_holds (key, reason, count, detail, created_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(key) DO UPDATE SET reason = excluded.reason, count = excluded.count, detail = excluded.detail, created_at = excluded.created_at`,
			x.Key, x.Reason, x.Count, x.Detail, created.UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("quiesce: set hold %s: %w", x.Key, err)
		}
	}
	return nil
}

func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} { // tsformat:allow: read-side parser for stored values
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func isNoSuchTable(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such table")
}
