package backup

// Bundle retention: which bundles a rotation keeps.
//
// The rule that shipped first dropped a bundle when it was beyond keepLast
// OR older than keepDays. The UI said "Always keep N most recent bundles",
// and the code did not: a workspace whose backups had stopped for longer
// than keepDays lost every copy on the next rotation — precisely when the
// old copies were the only ones left. RetentionPolicy replaces it:
//
//   - a floor that age never touches: the newest KeepMin bundles, plus the
//     newest KeepMin that have been checked (proof level >= 2) and recorded
//     no gaps, so a floor full of unchecked or partial copies still holds a
//     known-good one when one exists. The floor is never below 1 — no policy
//     deletes the last copy;
//   - a protected set no newer copy displaces: the newest complete bundle
//     whose test restore succeeded, and the newest complete bundle whose
//     contents were checked. A run that came out incomplete rotates too, and
//     a string of them must never push out the last copy known to be whole.
//     Off-site copies follow: only bundles a rotation drops lose theirs;
//   - pinned bundles, never deleted by any rule;
//   - grandfather-father-son beyond the floor: the newest bundle of each of
//     the last Daily days, Weekly ISO weeks and Monthly months;
//   - MaxAgeDays, the legacy keepDays: beyond the floor, anything younger is
//     kept.
//
// Rules apply per group — scope + workspace (+ crew for crew bundles, + plan
// when a bundle came from a plan) — so crew bundles never push workspace
// bundles out, and two plans never crowd each other.

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"time"
)

// RetentionPolicy says which bundles a rotation keeps. Zero values disable
// a rule; KeepMin below 1 is treated as 1.
type RetentionPolicy struct {
	KeepMin    int `json:"keep_min"`
	Daily      int `json:"keep_daily"`
	Weekly     int `json:"keep_weekly"`
	Monthly    int `json:"keep_monthly"`
	MaxAgeDays int `json:"max_age_days,omitempty"`
}

// LegacyRetentionPolicy maps the original (keepLast, keepDays) rotation
// onto a policy: keepLast becomes the floor, and keepDays applies only
// beyond it. With keepDays 0, everything beyond the floor goes (the old
// count cap); with keepLast 0 the floor is still 1.
func LegacyRetentionPolicy(keepLast, keepDays int) RetentionPolicy {
	return RetentionPolicy{KeepMin: keepLast, MaxAgeDays: keepDays}
}

// RetentionCandidate is one bundle a rotation considers.
type RetentionCandidate struct {
	Path        string
	Scope       Scope
	WorkspaceID string
	// CrewID separates crew-scope bundles of different crews.
	CrewID string
	// PlanID separates bundles made by different backup plans.
	PlanID     string
	CreatedAt  time.Time
	Pinned     bool
	ProofLevel int
	// DrillResult is the bundle's last test restore: ok | partial | failed,
	// "" when none ran.
	DrillResult string
	// Complete: the catalog recorded no gaps for the bundle. Incomplete: it
	// recorded some. Both false when the catalog does not know (no row, or a
	// row written before gaps were recorded).
	Complete   bool
	Incomplete bool
}

func (c RetentionCandidate) groupKey() string {
	crew := ""
	if c.Scope == ScopeCrew {
		crew = c.CrewID
	}
	return string(c.Scope) + "\x00" + c.WorkspaceID + "\x00" + crew + "\x00" + c.PlanID
}

// PlanRetention splits cands into keep and drop under p, as of now. Both
// slices preserve the input order.
func PlanRetention(cands []RetentionCandidate, p RetentionPolicy, now time.Time) (keep, drop []RetentionCandidate) {
	floor := p.KeepMin
	if floor < 1 {
		floor = 1
	}
	now = now.UTC()
	groups := map[string][]int{}
	for i, c := range cands {
		k := c.groupKey()
		groups[k] = append(groups[k], i)
	}
	kept := make([]bool, len(cands))
	for _, idx := range groups {
		sort.SliceStable(idx, func(a, b int) bool {
			return cands[idx[a]].CreatedAt.After(cands[idx[b]].CreatedAt)
		})
		checked := 0
		restoredOK, completeChecked := false, false
		days, weeks, months := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for rank, i := range idx {
			c := cands[i]
			at := c.CreatedAt.UTC()
			// A bundle with recorded gaps is never counted as a checked
			// copy: its check proved it opens, not that it holds everything.
			countsChecked := c.ProofLevel >= ProofContents && !c.Incomplete
			switch {
			case rank < floor, c.Pinned:
				kept[i] = true
			case countsChecked && checked < floor:
				kept[i] = true
			case p.MaxAgeDays > 0 && at.After(now.AddDate(0, 0, -p.MaxAgeDays)):
				kept[i] = true
			}
			if countsChecked && checked < floor {
				checked++
			}
			// The protected set, whatever newer copies exist: the newest
			// complete bundle whose test restore succeeded, and the newest
			// complete bundle whose contents were checked. Neither can be
			// displaced by newer partial or unchecked copies.
			if c.Complete && c.DrillResult == "ok" && !restoredOK {
				restoredOK = true
				kept[i] = true
			}
			if c.Complete && c.ProofLevel >= ProofContents && !completeChecked {
				completeChecked = true
				kept[i] = true
			}
			// GFS buckets: the first (newest) bundle seen in a bucket inside
			// its window claims it, whether or not the floor already kept it.
			if p.Daily > 0 {
				key := at.Format("2006-01-02")
				if !days[key] && !startOfDay(at).Before(startOfDay(now).AddDate(0, 0, -(p.Daily-1))) {
					days[key] = true
					kept[i] = true
				}
			}
			if p.Weekly > 0 {
				y, w := at.ISOWeek()
				key := fmt.Sprintf("%d-%02d", y, w)
				if !weeks[key] && !startOfISOWeek(at).Before(startOfISOWeek(now).AddDate(0, 0, -7*(p.Weekly-1))) {
					weeks[key] = true
					kept[i] = true
				}
			}
			if p.Monthly > 0 {
				key := at.Format("2006-01")
				if !months[key] && !startOfMonth(at).Before(startOfMonth(now).AddDate(0, -(p.Monthly-1), 0)) {
					months[key] = true
					kept[i] = true
				}
			}
		}
	}
	for i, c := range cands {
		if kept[i] {
			keep = append(keep, c)
		} else {
			drop = append(drop, c)
		}
	}
	return keep, drop
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func startOfISOWeek(t time.Time) time.Time {
	d := startOfDay(t)
	offset := (int(d.Weekday()) + 6) % 7 // Monday = 0
	return d.AddDate(0, 0, -offset)
}

func startOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// RotateWithPolicy applies p to the bundles in dir that belong to
// workspaceID and deletes what it drops (unless dryRun). db is optional:
// when set, backup_catalog supplies each bundle's pin, proof level and plan
// — without it nothing counts as pinned or checked. Returns the dropped
// paths, newest first.
func RotateWithPolicy(ctx context.Context, db *sql.DB, dir, workspaceID string, p RetentionPolicy, dryRun bool) ([]string, error) {
	return rotateWithPolicy(ctx, db, dir, workspaceID, p, dryRun, workspaceBundles(workspaceID), nil)
}

// workspaceBundles matches the bundles on disk that belong to workspaceID.
func workspaceBundles(workspaceID string) func(ListEntry) bool {
	return func(e ListEntry) bool { return e.WorkspaceID != "" && e.WorkspaceID == workspaceID }
}

// RotatePlanWithPolicy is RotateWithPolicy narrowed to the bundles one backup
// plan made (backup_catalog.plan_id = planID): a plan's keep rules apply to
// its own bundles and never to a manual bundle or another plan's. db is
// required — the catalog is the only place a bundle's plan is recorded.
func RotatePlanWithPolicy(ctx context.Context, db *sql.DB, dir, workspaceID, planID string, p RetentionPolicy, dryRun bool) ([]string, error) {
	if db == nil || planID == "" {
		return nil, fmt.Errorf("backup: rotate plan: a database and a plan id are required")
	}
	return rotateWithPolicy(ctx, db, dir, workspaceID, p, dryRun, workspaceBundles(workspaceID), func(c RetentionCandidate) bool { return c.PlanID == planID })
}

// RotateInstancePlanWithPolicy applies a plan's keep rules to the instance
// bundles that plan made: the same floor (keep_min, never below 1), pins,
// checked copies and grandfather-father-son buckets as a workspace plan.
// Deleted bundles release their environment layers and the store is
// collected, exactly as for workspace bundles. db is required.
func RotateInstancePlanWithPolicy(ctx context.Context, db *sql.DB, dir, planID string, p RetentionPolicy, dryRun bool) ([]string, error) {
	if db == nil || planID == "" {
		return nil, fmt.Errorf("backup: rotate plan: a database and a plan id are required")
	}
	return rotateWithPolicy(ctx, db, dir, "", p, dryRun,
		func(e ListEntry) bool { return e.Scope == ScopeInstance },
		func(c RetentionCandidate) bool { return c.PlanID == planID })
}

// rotateWithPolicy: match picks the bundles on disk to consider, only (when
// set) narrows them by what the catalog says; workspaceID narrows the
// catalog read ("" reads all of it).
func rotateWithPolicy(ctx context.Context, db *sql.DB, dir, workspaceID string, p RetentionPolicy, dryRun bool, match func(ListEntry) bool, only func(RetentionCandidate) bool) ([]string, error) {
	entries, err := ListBackups(ctx, dir)
	if err != nil {
		return nil, err
	}
	meta := map[string]CatalogEntry{}
	if db != nil {
		cat, err := ListCatalog(ctx, db, workspaceID)
		if err != nil {
			return nil, err
		}
		for _, e := range cat {
			meta[e.FilePath] = e
		}
	}
	var cands []RetentionCandidate
	for _, e := range entries {
		if !match(e) {
			continue
		}
		c := RetentionCandidate{
			Path: e.Path, Scope: e.Scope, WorkspaceID: e.WorkspaceID, CrewID: e.CrewID,
			CreatedAt: e.CreatedAt, ProofLevel: ProofChecksum,
		}
		if m, ok := meta[e.Path]; ok {
			c.Pinned = m.Pinned
			c.PlanID = m.PlanID
			c.DrillResult = m.DrillResult
			// nil Incomplete is "not recorded", never "complete".
			c.Complete = m.Incomplete != nil && len(m.Incomplete) == 0
			c.Incomplete = len(m.Incomplete) > 0
			if m.ProofLevel > 0 {
				c.ProofLevel = m.ProofLevel
			}
		}
		if only != nil && !only(c) {
			continue
		}
		cands = append(cands, c)
	}
	_, drop := PlanRetention(cands, p, time.Now())
	out := make([]string, 0, len(drop))
	for _, c := range drop {
		out = append(out, c.Path)
	}
	if dryRun {
		return out, nil
	}
	for _, path := range out {
		if err := Delete(ctx, path); err != nil {
			return out, err
		}
		// A deleted bundle leaves the catalog too, or the console keeps
		// listing a copy that no longer exists.
		if db != nil {
			if err := DeleteCatalogEntry(ctx, db, path); err != nil {
				return out, err
			}
		}
		// The bundle is gone: it no longer holds its environment layers.
		if err := ReleaseEnvironmentRefs(ctx, db, path); err != nil {
			return out, err
		}
	}
	if len(out) > 0 {
		if _, err := CollectEnvironmentGarbage(ctx, db, EnvironmentStoreFor(dir), time.Now()); err != nil {
			return out, fmt.Errorf("backup: collect environment layers: %w", err)
		}
	}
	return out, nil
}

// ReleaseBundleEnvironments is what deleting a bundle file owes the
// environment store: drop the bundle's refs, then collect the layers no
// other bundle needs.
func ReleaseBundleEnvironments(ctx context.Context, db *sql.DB, path string) error {
	if err := ReleaseEnvironmentRefs(ctx, db, path); err != nil {
		return err
	}
	_, err := CollectEnvironmentGarbage(ctx, db, EnvironmentStoreFor(filepath.Dir(path)), time.Now())
	return err
}
