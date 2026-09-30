package backupplan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/backup/offsite"
	"github.com/crewship-ai/crewship/internal/diskusage"
	"github.com/crewship-ai/crewship/internal/httpsafe"
)

// Track C2 in the run path: limits, the space floor, off-site copies,
// incidents and the heartbeat. service.go calls into these at the points
// named in each doc comment.

// Alerter delivers incidents to people: the router's implementation puts
// an inbox item in front of every instance admin. Raised is called when an
// incident opens or repeats (opened says which) and only for kinds the
// settings' events switch on; Resolved when it closes.
type Alerter interface {
	Raised(ctx context.Context, inc *Incident, opened bool, detail string)
	Resolved(ctx context.Context, inc *Incident)
}

// SpaceFunc reports the free and total bytes of the filesystem holding dir.
type SpaceFunc func(dir string) (free, total uint64, err error)

func diskSpace(dir string) (uint64, uint64, error) {
	for dir != "" && dir != string(filepath.Separator) {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		dir = filepath.Dir(dir)
	}
	u, err := diskusage.Usage(dir)
	if err != nil {
		return 0, 0, err
	}
	return u.FreeBytes, u.TotalBytes, nil
}

// MinFreeFraction: a run that would leave less than this share of the disk
// free does not start. MinFreePercentEnv overrides it (0-50, in percent) for
// a host whose disk is shared with other data, and for tests.
const MinFreeFraction = 0.10

// MinFreePercentEnv names the override of MinFreeFraction.
const MinFreePercentEnv = "CREWSHIP_BACKUP_MIN_FREE_PERCENT"

func minFreeFraction() float64 {
	if raw := strings.TrimSpace(os.Getenv(MinFreePercentEnv)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 && n <= 50 {
			return float64(n) / 100
		}
	}
	return MinFreeFraction
}

// StagingNeed estimates the room a run of this scope (and workspace) needs
// while it writes: the sealed staging file and the finished bundle coexist
// until the rename, so twice the largest recent bundle of the same scope
// (the last 14 days, else ever). Zero when nothing of the scope exists yet.
func StagingNeed(entries []backup.CatalogEntry, scope, workspaceID string, now time.Time) int64 {
	var recent, ever int64
	for _, e := range entries {
		if scope == ScopeInstance && e.Scope != string(backup.ScopeInstance) {
			continue
		}
		if scope == ScopeWorkspaces && (e.Scope != string(backup.ScopeWorkspace) || (workspaceID != "" && e.WorkspaceID != workspaceID)) {
			continue
		}
		if e.Size > ever {
			ever = e.Size
		}
		if now.Sub(e.CreatedAt) <= 14*24*time.Hour && e.Size > recent {
			recent = e.Size
		}
	}
	if recent == 0 {
		recent = ever
	}
	return int64(float64(recent) * StagingFactor)
}

// spaceRefusal says why a run must not start for lack of room, or "".
func (s *Service) spaceRefusal(ctx context.Context, r *Run) string {
	dir := s.backupsDir()
	if dir == "" {
		return ""
	}
	space := s.Space
	if space == nil {
		space = diskSpace
	}
	free, total, err := space(dir)
	if err != nil || total == 0 {
		return ""
	}
	entries, err := backup.ListCatalog(ctx, s.DB, "")
	if err != nil {
		return ""
	}
	need := StagingNeed(entries, r.Scope, r.WorkspaceID, s.now())
	frac := minFreeFraction()
	floor := uint64(float64(total) * frac)
	if uint64(need) <= free && free-uint64(need) >= floor {
		return ""
	}
	return fmt.Sprintf("not enough disk space: the run needs about %s for staging beside the finished backup, %s of %s is free, and a run that would leave less than %d%% free does not start",
		humanBytes(need), humanBytes(int64(free)), humanBytes(int64(total)), int(frac*100+0.5))
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp])
}

func (s *Service) backupsDir() string {
	if s.BackupsDir != nil {
		d, _ := s.BackupsDir()
		return d
	}
	d, _ := backup.DefaultBackupsDir()
	return d
}

func (s *Service) settings(ctx context.Context) Settings {
	set, err := LoadSettings(ctx, s.DB)
	if err != nil {
		return DefaultSettings()
	}
	return set
}

// ─── Incidents ──────────────────────────────────────────────────────────────

func (s *Service) raise(ctx context.Context, set Settings, planID, kind, message, runID, detail string, bump bool) {
	inc, opened, err := RaiseIncident(ctx, s.DB, planID, kind, message, runID, s.now(), bump)
	if err != nil {
		s.Logger.Warn("backup incident: raise", "plan", planID, "kind", kind, "error", err)
		return
	}
	if !opened && !bump {
		return // a persisting condition, already delivered
	}
	if s.Alerts != nil && set.Events.Wants(kind) {
		s.Alerts.Raised(ctx, inc, opened, detail)
	}
}

func (s *Service) resolve(ctx context.Context, planID string, kinds ...string) {
	closed, err := ResolveIncidents(ctx, s.DB, planID, kinds, s.now())
	if err != nil {
		s.Logger.Warn("backup incident: resolve", "plan", planID, "error", err)
		return
	}
	if s.Alerts != nil {
		for _, inc := range closed {
			s.Alerts.Resolved(ctx, inc)
		}
	}
}

// afterRun turns a run's terminal state into incidents: failed and skipped
// runs raise (or repeat) the plan's "failed" incident; an incomplete run
// raises "incomplete" and clears "failed" and "stale"; a done run clears all
// three and pings the heartbeat. Called once per terminal state.
func (s *Service) afterRun(ctx context.Context, r *Run, plan *Plan) {
	set := s.settings(ctx)
	label := incidentLabel(plan)
	last := Ago(lastGoodRun(ctx, s.DB, r.PlanID), s.now())
	switch r.Status {
	case StatusFailed, StatusSkipped, StatusInterrupted:
		verb := "failed"
		switch r.Status {
		case StatusSkipped:
			verb = "did not run"
		case StatusInterrupted:
			verb = "was interrupted twice"
		}
		msg := fmt.Sprintf("%s backup %s. Last successful backup: %s.", label, verb, last)
		s.raise(ctx, set, r.PlanID, IncidentFailed, msg, r.ID, r.Error, true)
	case StatusIncomplete:
		s.resolve(ctx, r.PlanID, IncidentFailed, IncidentStale)
		msg := fmt.Sprintf("%s backup is incomplete: %s.", label, describeIncomplete(r.Incomplete))
		s.raise(ctx, set, r.PlanID, IncidentIncomplete, msg, r.ID, "", true)
	case StatusDone:
		s.resolve(ctx, r.PlanID, IncidentFailed, IncidentIncomplete, IncidentStale)
		if set.HeartbeatURL != nil {
			s.pingHeartbeat(*set.HeartbeatURL)
		}
	}
}

// staleEvery is how often the scheduler tick looks for stale plans.
const staleEvery = 5 * time.Minute

// checkStale raises "stale" for every enabled plan whose newest good backup
// is older than the settings' stale_alert_hours (a plan that never had one
// counts from its creation), and clears it for the others. Runs from Tick,
// at most every staleEvery.
func (s *Service) checkStale(ctx context.Context) {
	now := s.now()
	s.mu.Lock()
	due := s.lastStale.IsZero() || now.Sub(s.lastStale) >= staleEvery
	if due {
		s.lastStale = now
	}
	s.mu.Unlock()
	if !due {
		return
	}
	plans, err := ListPlans(ctx, s.DB)
	if err != nil {
		return
	}
	set := s.settings(ctx)
	limit := time.Duration(set.StaleAlertHours) * time.Hour
	for _, p := range plans {
		if !p.Enabled {
			s.resolve(ctx, p.ID, IncidentStale)
			continue
		}
		last := lastGoodRun(ctx, s.DB, p.ID)
		since := last
		if since == nil {
			if t, err := parseTS(p.CreatedAt); err == nil {
				since = &t
			}
		}
		if since == nil || now.Sub(*since) <= limit {
			s.resolve(ctx, p.ID, IncidentStale)
			continue
		}
		msg := fmt.Sprintf("%s: no backup for over %d hours. Last successful backup: %s.", p.Name, set.StaleAlertHours, Ago(last, now))
		s.raise(ctx, set, p.ID, IncidentStale, msg, "", "", false)
	}
}

// RecordDrillOutcome raises the plan's "drill" incident for a failed or
// partial test restore and clears it for an ok one. planID "" is a bundle
// made without a plan. Called by the drills endpoint.
func (s *Service) RecordDrillOutcome(ctx context.Context, planID, result, detail string) {
	if result == "ok" {
		s.resolve(ctx, planID, IncidentDrill)
		return
	}
	label := "Manual"
	if p := s.planOf(ctx, planID); p != nil {
		label = p.Name
	}
	msg := fmt.Sprintf("A test restore of a %s backup was %s.", label, result)
	s.raise(ctx, s.settings(ctx), planID, IncidentDrill, msg, "", detail, true)
}

// ─── Heartbeat ──────────────────────────────────────────────────────────────

// pingHeartbeat GETs the heartbeat URL in the background (healthchecks.io
// style) after a good run: public https only, 10 s per attempt, one retry.
// It never blocks the run; the outcome is recorded in backup_settings.
func (s *Service) pingHeartbeat(raw string) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := s.ping(ctx, raw)
		status := "ok"
		if err != nil {
			status = err.Error()
			s.Logger.Warn("backup heartbeat ping failed", "error", err)
		}
		if rerr := recordHeartbeat(ctx, s.DB, s.now(), status); rerr != nil {
			s.Logger.Warn("backup heartbeat: record", "error", rerr)
		}
	}()
}

func (s *Service) ping(ctx context.Context, raw string) error {
	if !s.allowPrivateHeartbeat {
		if _, err := httpsafe.ValidateURL(raw, "https"); err != nil {
			return fmt.Errorf("heartbeat URL refused: %w", err)
		}
	}
	client := s.HeartbeatClient
	if client == nil {
		client = httpsafe.SafeClient(10*time.Second, "https")
	}
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			last = err
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		last = fmt.Errorf("heartbeat answered %d", resp.StatusCode)
	}
	return last
}

// ─── Off-site ───────────────────────────────────────────────────────────────

// offsiteTargets is the plan's destinations other than this server.
func offsiteTargets(plan *Plan) []string {
	if plan == nil {
		return nil
	}
	var out []string
	for _, d := range plan.Destinations {
		if d != "" && d != DestinationLocal {
			out = append(out, d)
		}
	}
	return out
}

// copyOffsite is the off-site phase: upload the finished bundle to every
// destination of the plan, through the shared upload limiter, and record a
// copy only once offsite.Upload has verified it at the destination. Returns
// the phase status ("done" | "failed" | "skipped") and its detail.
func (s *Service) copyOffsite(ctx context.Context, r *Run, plan *Plan, path string, upload *offsite.Limiter) (string, string) {
	targets := offsiteTargets(plan)
	if len(targets) == 0 {
		return "skipped", "not configured"
	}
	open := s.Destinations
	if open == nil {
		open = DBDestinations(s.DB)
	}
	var ok, failed []string
	for _, id := range targets {
		dst, d, err := open(ctx, id)
		name := id
		if d != nil {
			name = d.Name
		}
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		key := ObjectKey(r.Scope, r.WorkspaceID, path)
		obj, err := offsite.Upload(ctx, dst, path, key, offsite.UploadOptions{Limiter: upload})
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		if err := RecordCopy(ctx, s.DB, Copy{BundlePath: path, DestinationID: id, Key: obj.Key, Size: obj.Size, SHA256: obj.SHA256, VerifiedAt: ts(s.now())}); err != nil {
			failed = append(failed, fmt.Sprintf("%s: uploaded and verified, but the copy could not be recorded: %v", name, err))
			continue
		}
		ok = append(ok, name)
	}
	set := s.settings(ctx)
	if len(failed) > 0 {
		detail := strings.Join(failed, "; ")
		msg := fmt.Sprintf("%s backup has no off-site copy: the upload to %s failed.", incidentLabel(plan), strings.Join(failedNames(failed), ", "))
		s.raise(ctx, set, r.PlanID, IncidentOffsite, msg, r.ID, detail, true)
		if len(ok) > 0 {
			detail = "copied and verified to " + strings.Join(ok, ", ") + "; " + detail
		}
		return "failed", detail
	}
	s.resolve(ctx, r.PlanID, IncidentOffsite)
	return "done", "copied and verified to " + strings.Join(ok, ", ")
}

func failedNames(failed []string) []string {
	out := make([]string, 0, len(failed))
	for _, f := range failed {
		if i := strings.Index(f, ":"); i > 0 {
			out = append(out, f[:i])
		}
	}
	return out
}

// dropRemoteCopies deletes the off-site copies of bundles retention just
// removed, and their records. A destination that cannot be reached keeps its
// record, so the next rotation tries again.
func (s *Service) dropRemoteCopies(ctx context.Context, paths []string) {
	open := s.Destinations
	if open == nil {
		open = DBDestinations(s.DB)
	}
	for _, p := range paths {
		copies, err := CopiesOf(ctx, s.DB, p)
		if err != nil {
			continue
		}
		for _, c := range copies {
			dst, _, err := open(ctx, c.DestinationID)
			if err == nil {
				err = dst.Delete(ctx, c.Key)
			}
			if err != nil && !errors.Is(err, ErrNotFound) {
				s.Logger.Warn("backup retention: remote copy not deleted", "bundle", p, "destination", c.DestinationID, "error", err)
				continue
			}
			if _, err := s.DB.ExecContext(ctx, `DELETE FROM backup_copies WHERE bundle_path = ? AND destination_id = ?`, p, c.DestinationID); err != nil {
				s.Logger.Warn("backup retention: copy record", "error", err)
			}
		}
	}
}
