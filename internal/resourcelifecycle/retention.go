package resourcelifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/quiesce"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// RetentionContainer is one container on the daemon as idle retention sees it.
type RetentionContainer struct {
	ID         string
	State      string // docker state: running, exited, created, ...
	ImageID    string
	InstanceID string
	CrewID     string
	Kind       string
	// Provisioning marks a temporary build container of any installation.
	Provisioning bool
	// FinishedAt is when the container last stopped; zero when unknown or
	// never stopped. Only Inspect fills it.
	FinishedAt time.Time
	Mounts     []Mount
}

// CacheImage is one crewship-cache image with every tag it carries.
type CacheImage struct {
	ID      string
	Refs    []string
	Created time.Time
	Size    int64
}

// RetentionRuntime is the provider surface idle retention needs. Lists cover
// the whole daemon, every installation and every state.
type RetentionRuntime interface {
	ListContainers(context.Context) ([]RetentionContainer, error)
	InspectContainer(context.Context, string) (RetentionContainer, error)
	// RemoveIdleRuntime re-inspects the container under the crew's start
	// lock, lets verify veto on the fresh state, and only then removes it
	// with Force=false and RemoveVolumes=false.
	RemoveIdleRuntime(ctx context.Context, containerID, crewID string, verify func(RetentionContainer) error) error
	ListCacheImages(context.Context) ([]CacheImage, error)
	// RemoveImage untags/removes one reference with Force=false.
	RemoveImage(context.Context, string) error
}

// Retention removes stopped runtimes of live crews after a period of proven
// inactivity and evicts cache images no container on the daemon uses. It
// never removes volumes or host data, never touches deleted owners (that is
// Controller's job) and never acts on another installation's containers.
type Retention struct {
	DB         *sql.DB
	InstanceID string
	Runtime    RetentionRuntime
	Logger     *slog.Logger
	// RuntimeAfter is how long a runtime must have been stopped; 0 disables
	// runtime retention.
	RuntimeAfter time.Duration
	// EvictCache enables cache-image eviction.
	EvictCache bool
	// UnusedFor is how long an image must stay unused by every container
	// before eviction; MinImageAge protects freshly built images. Defaults
	// apply when zero.
	UnusedFor   time.Duration
	MinImageAge time.Duration
	Now         func() time.Time
	Interval    time.Duration
}

const (
	defaultUnusedFor   = time.Hour
	defaultMinImageAge = 24 * time.Hour
	defaultInterval    = 10 * time.Minute
)

var errNotEligible = errors.New("no longer eligible")

func (r *Retention) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Retention) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// Run ticks until ctx ends.
func (r *Retention) Run(ctx context.Context) {
	interval := r.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		r.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick runs one pass. Errors are logged; every decision is re-made next tick.
func (r *Retention) Tick(ctx context.Context) {
	if r == nil || r.DB == nil || r.Runtime == nil || r.InstanceID == "" {
		return
	}
	writer, ok := quiesce.Enter(ctx)
	if !ok {
		return
	}
	defer writer.Leave()
	ctx = writer.Context()
	if r.RuntimeAfter > 0 {
		r.runtimes(ctx)
	}
	if r.EvictCache {
		r.cache(ctx)
	}
}

// liveOwner reports whether crew is a live (not deleted) crew of this
// installation's database. A missing row is not live.
func (r *Retention) liveOwner(ctx context.Context, crew string) (bool, error) {
	var deleted sql.NullString
	err := r.DB.QueryRowContext(ctx, `SELECT deleted_at FROM crews WHERE id=?`, crew).Scan(&deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !deleted.Valid, nil
}

// idleEligible is the whole runtime rule, applied to the list view first and
// again to the fresh inspect under the crew lock.
func (r *Retention) idleEligible(c RetentionContainer) bool {
	return c.ID != "" && c.Kind == "crew" && c.InstanceID == r.InstanceID && c.CrewID != "" &&
		c.State == "exited" && !c.FinishedAt.IsZero() && r.now().Sub(c.FinishedAt) >= r.RuntimeAfter
}

func (r *Retention) runtimes(ctx context.Context) {
	list, err := r.Runtime.ListContainers(ctx)
	if err != nil {
		r.logger().Warn("idle retention: container list failed", "error", err)
		return
	}
	for _, c := range list {
		if quiesce.Yield(ctx) != nil {
			return
		}
		// Cheap filter on the list view; FinishedAt needs an inspect.
		if c.Kind != "crew" || c.InstanceID != r.InstanceID || c.CrewID == "" || c.State != "exited" {
			continue
		}
		stepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		r.runtime(stepCtx, c)
		cancel()
	}
}

func (r *Retention) runtime(ctx context.Context, c RetentionContainer) {
	inspected, err := r.Runtime.InspectContainer(ctx, c.ID)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			r.logger().Warn("idle retention: inspect failed", "container_id", c.ID, "error", err)
		}
		return
	}
	if !r.idleEligible(inspected) {
		return // running again, unknown stop time, or not idle long enough
	}
	if live, err := r.liveOwner(ctx, inspected.CrewID); err != nil || !live {
		return // deleted owners belong to Controller; unknown owners stay
	}
	err = r.Runtime.RemoveIdleRuntime(ctx, inspected.ID, inspected.CrewID, func(fresh RetentionContainer) error {
		// Under the crew's start lock: the container may have been started,
		// replaced or relabelled since the list, and the owner deleted.
		if fresh.ID != inspected.ID || !r.idleEligible(fresh) || !fresh.FinishedAt.Equal(inspected.FinishedAt) {
			return errNotEligible
		}
		if live, err := r.liveOwner(ctx, fresh.CrewID); err != nil || !live {
			return errNotEligible
		}
		b, err := mountSnapshot(fresh.Mounts)
		if err != nil {
			return fmt.Errorf("mount snapshot: %w", err)
		}
		_, err = r.DB.ExecContext(ctx, `INSERT INTO resource_cleanup_mounts(instance_id,crew_id,container_id,mounts_json,observed_at) VALUES(?,?,?,?,?) ON CONFLICT(instance_id,container_id) DO NOTHING`,
			r.InstanceID, fresh.CrewID, fresh.ID, string(b), tsformat.Format(r.now()))
		return err
	})
	switch {
	case err == nil:
		r.logger().Info("idle retention: removed stopped runtime, volumes kept",
			"crew_id", inspected.CrewID, "container_id", inspected.ID, "stopped_since", inspected.FinishedAt)
	case errors.Is(err, errNotEligible), errors.Is(err, ErrNotFound):
	default:
		r.logger().Warn("idle retention: runtime removal failed", "crew_id", inspected.CrewID, "container_id", inspected.ID, "error", err)
	}
}

func (r *Retention) cache(ctx context.Context) {
	containers, err := r.Runtime.ListContainers(ctx)
	if err != nil {
		r.logger().Warn("cache eviction: container list failed", "error", err)
		return
	}
	inUse := map[string]bool{}
	for _, c := range containers {
		if c.Provisioning {
			// A build of any installation is in flight: it may be about to
			// commit or reuse an image. Decide nothing this tick.
			r.logger().Info("cache eviction: provisioning in progress on the daemon; skipping this pass")
			return
		}
		inUse[c.ImageID] = true
	}
	images, err := r.Runtime.ListCacheImages(ctx)
	if err != nil {
		r.logger().Warn("cache eviction: image list failed", "error", err)
		return
	}
	seen, err := r.firstUnused(ctx)
	if err != nil {
		r.logger().Warn("cache eviction: state read failed", "error", err)
		return
	}
	unusedFor, minAge := r.UnusedFor, r.MinImageAge
	if unusedFor <= 0 {
		unusedFor = defaultUnusedFor
	}
	if minAge <= 0 {
		minAge = defaultMinImageAge
	}
	now := r.now()
	present := map[string]bool{}
	for _, img := range images {
		if quiesce.Yield(ctx) != nil {
			return
		}
		present[img.ID] = true
		if inUse[img.ID] {
			if _, tracked := seen[img.ID]; tracked {
				r.forget(ctx, img.ID) // any use restarts the unused window
			}
			continue
		}
		if now.Sub(img.Created) < minAge {
			continue
		}
		first, ok := seen[img.ID]
		if !ok {
			if _, err := r.DB.ExecContext(ctx, `INSERT INTO resource_retention_images(instance_id,image_id,first_unused_at) VALUES(?,?,?) ON CONFLICT(instance_id,image_id) DO NOTHING`,
				r.InstanceID, img.ID, tsformat.Format(now)); err != nil {
				r.logger().Warn("cache eviction: state write failed", "error", err)
			}
			continue
		}
		if now.Sub(first) < unusedFor {
			continue
		}
		r.evict(ctx, img)
	}
	for id := range seen {
		if !present[id] {
			r.forget(ctx, id)
		}
	}
}

func (r *Retention) evict(ctx context.Context, img CacheImage) {
	for _, ref := range img.Refs {
		if err := r.Runtime.RemoveImage(ctx, ref); err != nil {
			// In use after all (a container created since the list) or a
			// daemon error: keep it and start the window again.
			r.logger().Info("cache eviction: image kept", "ref", ref, "error", err)
			r.forget(ctx, img.ID)
			return
		}
	}
	r.forget(ctx, img.ID)
	r.logger().Info("cache eviction: removed unused cache image; the next start rebuilds it",
		"refs", img.Refs, "image_id", img.ID, "size_bytes", img.Size)
}

func (r *Retention) forget(ctx context.Context, imageID string) {
	if _, err := r.DB.ExecContext(ctx, `DELETE FROM resource_retention_images WHERE instance_id=? AND image_id=?`, r.InstanceID, imageID); err != nil {
		r.logger().Warn("cache eviction: state write failed", "error", err)
	}
}

func (r *Retention) firstUnused(ctx context.Context) (map[string]time.Time, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT image_id, first_unused_at FROM resource_retention_images WHERE instance_id=?`, r.InstanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id, at string
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			continue // unreadable bookkeeping restarts the window
		}
		out[id] = t
	}
	return out, rows.Err()
}
