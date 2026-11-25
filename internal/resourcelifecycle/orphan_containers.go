package resourcelifecycle

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

const orphanContainerWindow = 24 * time.Hour
const orphanContainerInterval = 30 * time.Minute

// A removal is possible only after complete, consecutive observations by this
// live process. Restored databases and restarted servers begin with no credit.
type orphanObservations struct {
	generation uint64
	at         time.Time
	first      map[string]time.Time
}

type orphanRuntime interface {
	// OrphanRetentionReason re-inspects the immutable container ID and
	// returns "" only when the runtime positively verified safe removal.
	OrphanRetentionReason(context.Context, Container) (string, error)
}

func (c *Controller) observeMissingOwners(ctx context.Context, rt Runtime, containers []Container, previous orphanObservations, budget int) {
	enabled, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("CREWSHIP_IMAGE_GC")))
	inspector, ok := rt.(orphanRuntime)
	if !enabled || !ok {
		return
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	generation := Images(c.DB).restoreGeneration.Load()
	gap := now.Sub(previous.at)
	if previous.generation != generation || gap < 0 || gap > 4*orphanContainerInterval {
		previous = orphanObservations{}
	} else if gap < orphanContainerInterval {
		c.orphans = previous
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rows, err := c.DB.QueryContext(ctx, "SELECT id FROM crews")
	if err != nil {
		return
	}
	owners := map[string]bool{}
	for rows.Next() {
		var crew string
		if rows.Scan(&crew) != nil || crew == "" {
			rows.Close()
			return
		}
		owners[crew] = true
	}
	err = rows.Err()
	if rows.Close() != nil || err != nil {
		return
	}
	seen := map[string]bool{}
	type candidate struct {
		container Container
		since     time.Time
	}
	var candidates []candidate
	for _, x := range containers {
		// Missing IDs or duplicates mean this was not a usable inventory.
		if x.ID == "" || seen[x.ID] {
			return
		}
		seen[x.ID] = true
		if x.CrewID == "" || owners[x.CrewID] || !eligible(x, c.InstanceID, x.CrewID) {
			continue
		}
		reason, err := inspector.OrphanRetentionReason(ctx, x)
		if err != nil {
			slog.Warn("orphan container observation incomplete", "container_id", x.ID)
			return
		}
		if reason != "" {
			slog.Info("orphan container retained", "container_id", x.ID, "reason", reason)
			continue
		}
		first, ok := previous.first[x.ID]
		if !ok {
			first = now
		}
		candidates = append(candidates, candidate{x, first})
	}
	next := orphanObservations{generation: generation, at: now, first: map[string]time.Time{}}
	for _, candidate := range candidates {
		x := candidate.container
		if now.Sub(candidate.since) < orphanContainerWindow || budget <= 0 {
			next.first[x.ID] = candidate.since
			continue
		}
		removed, err := c.removeMissingOwner(ctx, rt, inspector, x, generation)
		if err != nil {
			slog.Warn("orphan container removal deferred", "container_id", x.ID)
			return
		}
		if removed {
			budget--
			slog.Info("removed unreferenced orphan container", "container_id", x.ID)
		}
		// Busy, changed or revived: restart the full observation window.
	}
	c.orphans = next
}

func (c *Controller) removeMissingOwner(ctx context.Context, rt Runtime, inspector orphanRuntime, x Container, generation uint64) (bool, error) {
	g := Images(c.DB)
	release, ok := g.TryCollect()
	if !ok {
		return false, nil
	}
	defer release()
	if g.restoreGeneration.Load() != generation {
		return false, nil
	}
	// An image build may use only per-alias reservations. Keep new builds
	// out until the non-force remove has completed, including unidentified
	// base images. Restoration/crew edits use the outer gate.
	if !g.provisioning.TryLock() {
		return false, nil
	}
	defer g.provisioning.Unlock()
	releaseImages, idle := g.TryCollectAllImages()
	if !idle {
		return false, nil
	}
	defer releaseImages()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// Obtain SQLite writer admission before checking a negative fact. A crew
	// inserted by any other caller cannot race the check and daemon removal.
	if _, err = tx.ExecContext(ctx, "UPDATE crews SET deleted_at=deleted_at WHERE 0"); err != nil {
		return false, err
	}
	var present int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM crews WHERE id=?", x.CrewID).Scan(&present)
	if err == nil {
		return false, nil
	}
	if err != sql.ErrNoRows {
		return false, err
	}
	reason, err := inspector.OrphanRetentionReason(ctx, x)
	if err != nil || reason != "" {
		return false, err
	}
	if err = rt.Remove(ctx, x.ID); err != nil {
		return false, err
	}
	return true, nil
}
