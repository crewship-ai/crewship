package backupplan

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/diskusage"
)

// Overview wire types — the console's OverviewResponse.

type StatusRow struct {
	Value      string  `json:"value"`
	Detail     *string `json:"detail"`
	DetailTone *string `json:"detail_tone"`
}

type OverviewStatus struct {
	Label           string    `json:"label"`
	Verdict         string    `json:"verdict"`
	Summary         *string   `json:"summary"`
	Protects        StatusRow `json:"protects"`
	HowOften        StatusRow `json:"how_often"`
	Where           StatusRow `json:"where"`
	HowLong         StatusRow `json:"how_long"`
	ReallyRestored  StatusRow `json:"really_restored"`
	OffsiteVerified bool      `json:"offsite_verified"`
}

type AttentionAction struct {
	Kind        string  `json:"kind"`
	Label       string  `json:"label"`
	WorkspaceID *string `json:"workspace_id"`
	RunID       *string `json:"run_id"`
}

type AttentionItem struct {
	ID       string           `json:"id"`
	Severity string           `json:"severity"` // bad | warn
	Title    string           `json:"title"`
	Detail   string           `json:"detail"`
	Action   *AttentionAction `json:"action"`
}

type Night struct {
	Date   string  `json:"date"`
	Status string  `json:"status"` // ok | incomplete | skipped | late | failed | none
	Proof  int     `json:"proof"`
	Detail *string `json:"detail"`
}

type SpaceInfo struct {
	BackupsBytes     int64 `json:"backups_bytes"`
	FreeBytes        int64 `json:"free_bytes"`
	TotalBytes       int64 `json:"total_bytes"`
	StagingNeedBytes int64 `json:"staging_need_bytes"`
	RestoreNeedBytes int64 `json:"restore_need_bytes"`
	// MinFreePercent is the space floor in force: a run that would leave
	// less than this share of the disk free does not start.
	MinFreePercent int `json:"min_free_percent"`
	// Refusal is why the next run would not start for lack of room, with
	// what to do about it; null when it would.
	Refusal *string `json:"refusal"`
}

type WorkspaceCoverage struct {
	WorkspaceID  string  `json:"workspace_id"`
	Name         string  `json:"name"`
	LastBackupAt *string `json:"last_backup_at"`
	Status       string  `json:"status"` // ok | warn | bad
	Plan         *string `json:"plan"`
	Proof        int     `json:"proof"`
}

type OverviewResponse struct {
	Status          OverviewStatus      `json:"status"`
	NeedsAttention  []AttentionItem     `json:"needs_attention"`
	Nights          []Night             `json:"nights"`
	Space           SpaceInfo           `json:"space"`
	Workspaces      []WorkspaceCoverage `json:"workspaces,omitempty"`
	InstanceSummary *string             `json:"instance_summary"`
}

// Verdicts (the console words them: verdictHeadline).
const (
	VerdictVerified        = "verified"
	VerdictPartial         = "partial"
	VerdictFailed          = "failed"
	VerdictContentsChecked = "contents_checked"
	VerdictChecksumOnly    = "checksum_only"
	VerdictNone            = "none"
)

var verdictRank = map[string]int{VerdictNone: 0, VerdictFailed: 1, VerdictChecksumOnly: 2, VerdictContentsChecked: 3, VerdictPartial: 4, VerdictVerified: 5}

const (
	// StagingFactor: a run's sealed staging file and the finished bundle
	// coexist until the rename, so a run needs about twice the largest
	// recent bundle of its scope.
	StagingFactor = 2.0
	// RestoreExpansion estimates how much a bundle grows when restored: the
	// payload is zstd-compressed JSON rows and crew files, which compress
	// about 3:1 in the bundles we have measured; a restore also stages the
	// decrypted payload. 3× the bundle is the planning figure.
	RestoreExpansion = 3
)

// KitInfo says which vault key versions the latest instance backup cannot
// unlock on a new server (the recovery kit is off, or a key was not
// resolvable when it ran), and how many sealed values use them. Nil: the
// overview says nothing about keys.
type KitInfo interface {
	MissingVaultKeys(ctx context.Context) (missing []string, sealed int, err error)
}

// OverviewInput selects what the overview covers.
type OverviewInput struct {
	Scope string // instance | workspaces
	// WorkspaceIDs narrows scope=workspaces; nil means every workspace.
	WorkspaceIDs []string
	BackupsDir   string
	Kit          KitInfo
	// Space overrides the disk reading (tests): free and total bytes.
	Space func() (free, total uint64)
}

func tone(t string) *string { return &t }

// kitDetail: "Key versions v1 and v2 are not in it; restoring elsewhere
// means re-entering 41 sealed values."
func kitDetail(missing []string, sealed int) string {
	var list string
	switch n := len(missing); n {
	case 1:
		list = "Key version " + missing[0] + " is"
	default:
		list = "Key versions " + strings.Join(missing[:n-1], ", ") + " and " + missing[n-1] + " are"
	}
	return fmt.Sprintf("%s not in it; restoring elsewhere means re-entering %d sealed value(s) (credentials, webhook secrets, integration keys).", list, sealed)
}

func bundleVerdict(e *backup.CatalogEntry) string {
	if e == nil {
		return VerdictNone
	}
	switch {
	case e.ProofLevel >= backup.ProofRestore:
		switch e.DrillResult {
		case "failed":
			return VerdictFailed
		case "partial":
			return VerdictPartial
		}
		if len(e.Incomplete) > 0 {
			return VerdictPartial
		}
		return VerdictVerified
	case e.ProofLevel >= backup.ProofContents:
		return VerdictContentsChecked
	default:
		return VerdictChecksumOnly
	}
}

type wsRef struct{ ID, Name string }

func liveWorkspaces(ctx context.Context, db *sql.DB) ([]wsRef, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name FROM workspaces WHERE deleted_at IS NULL ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []wsRef
	for rows.Next() {
		var w wsRef
		if err := rows.Scan(&w.ID, &w.Name); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// covers reports whether a full (non-custom), enabled plan backs up ws.
func covers(p *Plan, ws string) bool {
	if !p.Enabled || p.Preset == backup.PresetCustom {
		return false
	}
	if p.Scope == ScopeInstance {
		// Complete recovery covers every workspace once instance bundles
		// exist; until then its runs fail and the overview says so.
		return true
	}
	if len(p.WorkspaceIDs) == 0 {
		return true
	}
	for _, id := range p.WorkspaceIDs {
		if id == ws {
			return true
		}
	}
	return false
}

func fmtDay(t time.Time, loc *time.Location) string { return t.In(loc).Format("2 Jan 15:04") }

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

var incompleteNoun = map[string][2]string{
	backup.IncompleteAttachmentMissing:  {"attachment file missing", "attachment files missing"},
	backup.IncompleteMemoryBlobMissing:  {"memory version without content", "memory versions without content"},
	backup.IncompleteContainerMissing:   {"crew without its container", "crews without their containers"},
	backup.IncompleteCrewSectionFailed:  {"crew folder Docker could not copy", "crew folders Docker could not copy"},
	backup.IncompleteFileUnreadable:     {"file the server could not read", "files the server could not read"},
	backup.IncompleteEnvironmentFailed:  {"container environment not captured", "container environments not captured"},
	backup.IncompleteVaultKeyMissing:    {"vault key missing", "vault keys missing"},
	backup.IncompleteAttachmentConflict: {"attachment file in conflict", "attachment files in conflict"},
	backup.IncompleteFileMissing:        {"file missing", "files missing"},
	backup.IncompleteFileMismatch:       {"file that does not match", "files that do not match"},
	backup.IncompleteCredentialLocked:   {"credential that does not unlock", "credentials that do not unlock"},
}

// describeIncomplete sums the items per kind, in the order the kinds first
// appear, and names each in words ("2 crews without their containers"), never
// by its code.
func describeIncomplete(items []backup.IncompleteItem) string {
	var order []string
	sum := map[string]int{}
	for _, it := range items {
		if _, seen := sum[it.Kind]; !seen {
			order = append(order, it.Kind)
		}
		n := it.Count
		if n < 1 {
			n = 1
		}
		sum[it.Kind] += n
	}
	parts := make([]string, 0, len(order))
	for _, k := range order {
		if n, ok := incompleteNoun[k]; ok {
			parts = append(parts, plural(sum[k], n[0], n[1]))
			continue
		}
		parts = append(parts, fmt.Sprintf("%d × %s", sum[k], strings.ReplaceAll(k, "_", " ")))
	}
	return strings.Join(parts, ", ")
}

// BuildOverview composes the Overview from plans, runs and the catalog.
func BuildOverview(ctx context.Context, db *sql.DB, in OverviewInput, now time.Time) (*OverviewResponse, error) {
	now = now.UTC()
	plans, err := ListPlans(ctx, db)
	if err != nil {
		return nil, err
	}
	all, err := liveWorkspaces(ctx, db)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, w := range all {
		names[w.ID] = w.Name
	}
	selected := all
	if in.Scope == ScopeWorkspaces && in.WorkspaceIDs != nil {
		want := map[string]bool{}
		for _, id := range in.WorkspaceIDs {
			want[id] = true
		}
		selected = nil
		for _, w := range all {
			if want[w.ID] {
				selected = append(selected, w)
			}
		}
	}
	cat, err := backup.ListCatalog(ctx, db, "")
	if err != nil {
		return nil, err
	}
	// Protection bundles: full (never custom) bundles of the scope.
	inScope := func(e backup.CatalogEntry) bool {
		if e.Kind == backup.KindCustom {
			return false
		}
		if in.Scope == ScopeInstance {
			return e.Scope == string(backup.ScopeInstance)
		}
		if e.Scope != string(backup.ScopeWorkspace) {
			return false
		}
		for _, w := range selected {
			if w.ID == e.WorkspaceID {
				return true
			}
		}
		return false
	}
	var scoped []backup.CatalogEntry
	latestByWS := map[string]*backup.CatalogEntry{}
	var backupsBytes, largest, largestRecent int64
	for i := range cat {
		e := cat[i]
		backupsBytes += e.Size
		if e.Size > largest {
			largest = e.Size
		}
		if now.Sub(e.CreatedAt) <= 14*24*time.Hour && e.Size > largestRecent {
			largestRecent = e.Size
		}
		if !inScope(e) {
			continue
		}
		scoped = append(scoped, e)
		if cur := latestByWS[e.WorkspaceID]; cur == nil || e.CreatedAt.After(cur.CreatedAt) {
			latestByWS[e.WorkspaceID] = &cat[i]
		}
	}
	// Plans that cover the selection (full, enabled).
	var covering []*Plan
	for _, p := range plans {
		if !p.Enabled || p.Preset == backup.PresetCustom {
			continue
		}
		if in.Scope == ScopeInstance {
			if p.Scope == ScopeInstance {
				covering = append(covering, p)
			}
			continue
		}
		for _, w := range selected {
			if covers(p, w.ID) {
				covering = append(covering, p)
				break
			}
		}
	}
	loc := time.UTC
	if len(covering) > 0 {
		loc = covering[0].Location()
	}
	staleHours := 36
	if len(covering) > 0 {
		staleHours = covering[0].StaleAlert
	}

	out := &OverviewResponse{NeedsAttention: []AttentionItem{}, Nights: []Night{}}

	// ── Verdict: the weakest of the selection's latest bundles.
	var latest *backup.CatalogEntry
	verdict := VerdictVerified
	if in.Scope == ScopeInstance {
		for i := range scoped {
			if latest == nil || scoped[i].CreatedAt.After(latest.CreatedAt) {
				latest = &scoped[i]
			}
		}
		verdict = bundleVerdict(latest)
	} else {
		if len(selected) == 0 {
			verdict = VerdictNone
		}
		for _, w := range selected {
			e := latestByWS[w.ID]
			if v := bundleVerdict(e); verdictRank[v] < verdictRank[verdict] {
				verdict = v
			}
			if e != nil && (latest == nil || e.CreatedAt.After(latest.CreatedAt)) {
				latest = e
			}
		}
	}
	st := &out.Status
	st.Verdict = verdict
	if in.Scope == ScopeInstance {
		st.Label = "Complete recovery"
	} else if len(covering) > 0 {
		st.Label = covering[0].Name
	} else {
		st.Label = "Workspace backups"
	}
	switch {
	case in.Scope == ScopeInstance && latest == nil:
		withWS := 0
		for _, w := range all {
			for _, e := range cat {
				if e.WorkspaceID == w.ID && e.Kind != backup.KindCustom {
					withWS++
					break
				}
			}
		}
		s := fmt.Sprintf("No instance backup exists yet; %d of %d workspaces have workspace backups.", withWS, len(all))
		st.Summary = &s
	case latest != nil && len(latest.Incomplete) > 0:
		s := fmt.Sprintf("The latest backup (%s) is missing things: %s.", fmtDay(latest.CreatedAt, loc), describeIncomplete(latest.Incomplete))
		st.Summary = &s
	case latest != nil && latest.Incomplete == nil:
		s := "The latest backup was catalogued before gaps were recorded; check its contents to know it is complete."
		st.Summary = &s
	}

	// protects
	if in.Scope == ScopeInstance {
		if len(covering) > 0 {
			st.Protects = StatusRow{Value: "The whole instance", Detail: strp("every workspace, users and settings"), DetailTone: nil}
		} else {
			st.Protects = StatusRow{Value: "Nothing on a schedule", Detail: strp("no Complete recovery plan"), DetailTone: tone("bad")}
		}
	} else {
		n := 0
		for _, w := range selected {
			for _, p := range plans {
				if covers(p, w.ID) {
					n++
					break
				}
			}
		}
		row := StatusRow{Value: fmt.Sprintf("%d of %d workspaces on a plan", n, len(selected))}
		if n < len(selected) {
			row.Detail, row.DetailTone = strp(plural(len(selected)-n, "workspace has no plan", "workspaces have no plan")), tone("warn")
		}
		for _, p := range plans {
			if p.Enabled && p.Preset == backup.PresetCustom {
				row.Detail = strp(joinDetail(row.Detail, "custom plans do not count as protection"))
				break
			}
		}
		st.Protects = row
	}
	// how often / how long
	if len(covering) > 0 {
		p := covering[0]
		st.HowOften = StatusRow{Value: describeWhen(p), Detail: strp(p.Timezone)}
		if p.NextRunAt != nil {
			if t, err := parseTS(*p.NextRunAt); err == nil {
				st.HowOften.Detail = strp(fmt.Sprintf("%s · next %s", p.Timezone, fmtDay(t, loc)))
			}
		}
		st.HowLong = StatusRow{Value: describeKeep(p), Detail: strp("the newest copies are never aged out")}
	} else {
		st.HowOften = StatusRow{Value: "Not scheduled", Detail: strp("only backups made by hand"), DetailTone: tone("warn")}
		st.HowLong = StatusRow{Value: "Until deleted by hand", Detail: strp("no plan keeps or rotates copies"), DetailTone: tone("warn")}
	}
	// where
	where := "This server"
	if in.BackupsDir != "" {
		where = "This server (" + in.BackupsDir + ")"
	}
	st.Where = StatusRow{Value: where, Detail: strp("no off-site copy"), DetailTone: tone("warn")}
	// Off-site: verified only when every recent bundle of the scope (the
	// last 14 days, or the latest one) has a copy that was checked at the
	// destination after upload.
	copied := copiedPaths(ctx, db)
	var recentScoped, recentCopied int
	for _, e := range scoped {
		if now.Sub(e.CreatedAt) > 14*24*time.Hour && (latest == nil || e.FilePath != latest.FilePath) {
			continue
		}
		recentScoped++
		if copied[e.FilePath] {
			recentCopied++
		}
	}
	offsiteNames := map[string]bool{}
	var names2 []string
	if dests, err := ListDestinations(ctx, db); err == nil {
		byID := map[string]string{}
		for _, d := range dests {
			byID[d.ID] = d.Name
		}
		for _, p := range covering {
			for _, id := range offsiteTargets(p) {
				if n, ok := byID[id]; ok && !offsiteNames[n] {
					offsiteNames[n] = true
					names2 = append(names2, n)
				}
			}
		}
	}
	switch {
	case recentScoped > 0 && recentCopied == recentScoped:
		st.OffsiteVerified = true
		value := "This server and off-site"
		if len(names2) > 0 {
			value = "This server and " + strings.Join(names2, ", ")
		}
		st.Where = StatusRow{Value: value, Detail: strp("every recent backup has an off-site copy, checked after upload")}
	case len(names2) > 0 && recentScoped > 0:
		st.Where = StatusRow{Value: "This server and " + strings.Join(names2, ", "),
			Detail:     strp(fmt.Sprintf("%d of %d recent backups have a checked off-site copy", recentCopied, recentScoped)),
			DetailTone: tone("warn")}
	case len(names2) > 0:
		st.Where = StatusRow{Value: "This server and " + strings.Join(names2, ", "), Detail: strp("no backup copied yet"), DetailTone: tone("warn")}
	}
	// really restored
	var drilled *backup.CatalogEntry
	for i := range scoped {
		e := &scoped[i]
		if e.DrillAt != nil && (drilled == nil || e.DrillAt.After(*drilled.DrillAt)) {
			drilled = e
		}
	}
	if drilled == nil {
		st.ReallyRestored = StatusRow{Value: "Never test-restored", Detail: strp("a checksum is not a restore"), DetailTone: tone("warn")}
	} else {
		t := "ok"
		switch drilled.DrillResult {
		case "partial":
			t = "warn"
		case "failed":
			t = "bad"
		}
		st.ReallyRestored = StatusRow{Value: fmt.Sprintf("Test restore %s on %s", drilled.DrillResult, fmtDay(*drilled.DrillAt, loc)), DetailTone: tone(t)}
		if note := drillNote(drilled.DrillReport); note != nil {
			st.ReallyRestored.Detail = note
		}
	}

	// ── Runs of the last 15 days, in scope, full only.
	since := now.AddDate(0, 0, -15)
	runFilter := RunFilter{Since: &since, Scope: in.Scope}
	if in.Scope == ScopeWorkspaces {
		for _, w := range selected {
			runFilter.WorkspaceIDs = append(runFilter.WorkspaceIDs, w.ID)
		}
	}
	var runs []*Run
	if in.Scope == ScopeInstance || len(runFilter.WorkspaceIDs) > 0 {
		all, err := ListRuns(ctx, db, runFilter)
		if err != nil {
			return nil, err
		}
		for _, r := range all {
			if r.Kind != backup.KindCustom {
				runs = append(runs, r)
			}
		}
	}

	// ── Needs attention.
	add := func(it AttentionItem) { out.NeedsAttention = append(out.NeedsAttention, it) }
	if in.Scope == ScopeWorkspaces {
		for _, w := range selected {
			wsID := w.ID
			e := latestByWS[w.ID]
			if e != nil && len(e.Incomplete) > 0 {
				add(AttentionItem{ID: "incomplete:" + w.ID, Severity: "warn", Title: fmt.Sprintf("Latest backup of %s is incomplete", w.Name),
					Detail: describeIncomplete(e.Incomplete), Action: &AttentionAction{Kind: "see_which", Label: "See which", WorkspaceID: &wsID}})
			}
			switch {
			case e == nil:
				add(AttentionItem{ID: "never:" + w.ID, Severity: "bad", Title: fmt.Sprintf("%s has never been backed up", w.Name),
					Detail: "no bundle of this workspace exists", Action: &AttentionAction{Kind: "back_up_now", Label: "Back up now", WorkspaceID: &wsID}})
			case now.Sub(e.CreatedAt) > time.Duration(staleHours)*time.Hour:
				add(AttentionItem{ID: "stale:" + w.ID, Severity: "warn", Title: fmt.Sprintf("No recent backup of %s", w.Name),
					Detail: fmt.Sprintf("the newest is from %s, older than %d hours", fmtDay(e.CreatedAt, loc), staleHours),
					Action: &AttentionAction{Kind: "back_up_now", Label: "Back up now", WorkspaceID: &wsID}})
			}
			planned := false
			for _, p := range plans {
				if covers(p, w.ID) {
					planned = true
					break
				}
			}
			if !planned {
				add(AttentionItem{ID: "noplan:" + w.ID, Severity: "warn", Title: fmt.Sprintf("%s is on no backup plan", w.Name),
					Detail: "it is backed up only by hand", Action: &AttentionAction{Kind: "schedules", Label: "Add to a plan", WorkspaceID: &wsID}})
			}
		}
	} else {
		if latest != nil && len(latest.Incomplete) > 0 {
			add(AttentionItem{ID: "incomplete:instance", Severity: "warn", Title: "The latest instance backup is incomplete",
				Detail: describeIncomplete(latest.Incomplete), Action: &AttentionAction{Kind: "see_which", Label: "See which"}})
		}
		if len(covering) == 0 {
			add(AttentionItem{ID: "noplan:instance", Severity: "bad", Title: "No Complete recovery plan",
				Detail: "nothing backs up the whole instance on a schedule", Action: &AttentionAction{Kind: "schedules", Label: "Create a plan"}})
		}
	}
	seenRun := map[string]bool{}
	for _, r := range runs {
		runID := r.ID
		var wsID *string
		label := "the instance"
		if r.WorkspaceID != "" {
			wsID = optStr(r.WorkspaceID)
			label = names[r.WorkspaceID]
			if label == "" {
				label = r.WorkspaceID
			}
		}
		at := r.StartedAt
		if r.DueAt != nil {
			at = *r.DueAt
		}
		detail := r.Error
		if detail == "" {
			detail = "no reason recorded"
		}
		key := r.PlanID + "|" + r.WorkspaceID
		if seenRun[key] {
			continue // the newest run of each plan and target speaks for it
		}
		seenRun[key] = true
		switch {
		case r.Status == StatusFailed || (r.Status == StatusInterrupted && r.RetriedAt == nil):
			add(AttentionItem{ID: "run:" + r.ID, Severity: "bad", Title: fmt.Sprintf("Backup of %s failed on %s", label, fmtDay(at, loc)),
				Detail: detail, Action: &AttentionAction{Kind: "history", Label: "View run", WorkspaceID: wsID, RunID: &runID}})
		case r.Status == StatusSkipped:
			add(AttentionItem{ID: "run:" + r.ID, Severity: "warn", Title: fmt.Sprintf("Backup of %s skipped on %s", label, fmtDay(at, loc)),
				Detail: detail, Action: &AttentionAction{Kind: "history", Label: "View run", WorkspaceID: wsID, RunID: &runID}})
		case (r.Status == StatusDone || r.Status == StatusIncomplete) && r.Trigger == TriggerCatchup:
			add(AttentionItem{ID: "run:" + r.ID, Severity: "warn", Title: fmt.Sprintf("Backup of %s ran late on %s", label, fmtDay(at, loc)),
				Detail: "a catch-up after the scheduled time was missed", Action: &AttentionAction{Kind: "history", Label: "View run", WorkspaceID: wsID, RunID: &runID}})
		}
	}
	if in.Kit != nil && in.Scope == ScopeInstance {
		if missing, sealed, err := in.Kit.MissingVaultKeys(ctx); err == nil && len(missing) > 0 {
			add(AttentionItem{ID: "vault", Severity: "bad", Title: "On a new server the latest backup cannot unlock credentials",
				Detail: kitDetail(missing, sealed), Action: &AttentionAction{Kind: "keys", Label: "Keys"}})
		}
	}
	// ── Nights.
	out.Nights = buildNights(runs, scoped, now, loc)

	// ── Space.
	out.Space = SpaceInfo{BackupsBytes: backupsBytes}
	if largestRecent == 0 {
		largestRecent = largest
	}
	out.Space.StagingNeedBytes = StagingNeed(cat, in.Scope, "", now)
	if out.Space.StagingNeedBytes == 0 {
		out.Space.StagingNeedBytes = int64(float64(largestRecent) * StagingFactor)
	}
	out.Space.RestoreNeedBytes = largest * RestoreExpansion
	if in.BackupsDir != "" {
		dir := in.BackupsDir
		for dir != "" && dir != string(filepath.Separator) {
			if _, err := os.Stat(dir); err == nil {
				break
			}
			dir = filepath.Dir(dir)
		}
		if u, err := diskusage.Usage(dir); err == nil {
			out.Space.FreeBytes, out.Space.TotalBytes = int64(u.FreeBytes), int64(u.TotalBytes)
		}
	}
	out.Space.MinFreePercent = MinFreePercent()
	if in.Space != nil {
		free, total := in.Space()
		out.Space.FreeBytes, out.Space.TotalBytes = int64(free), int64(total)
	}
	if out.Space.TotalBytes > 0 {
		if reason := SpaceFloorRefusal(uint64(out.Space.FreeBytes), uint64(out.Space.TotalBytes), out.Space.StagingNeedBytes); reason != "" {
			out.Space.Refusal = &reason
			used := 100 - int(float64(out.Space.FreeBytes)*100/float64(out.Space.TotalBytes))
			add(AttentionItem{ID: "space", Severity: "bad", Title: fmt.Sprintf("Backups will not start: the disk is %d%% full", used),
				Detail: reason, Action: &AttentionAction{Kind: "storage", Label: "Storage"}})
		}
	}
	sort.SliceStable(out.NeedsAttention, func(i, j int) bool {
		return out.NeedsAttention[i].Severity == "bad" && out.NeedsAttention[j].Severity != "bad"
	})

	// ── Per-workspace rows.
	if in.Scope == ScopeWorkspaces {
		out.Workspaces = []WorkspaceCoverage{}
		for _, w := range selected {
			row := WorkspaceCoverage{WorkspaceID: w.ID, Name: w.Name, Status: "ok"}
			for _, p := range plans {
				if covers(p, w.ID) {
					row.Plan = strp(p.Name)
					break
				}
			}
			e := latestByWS[w.ID]
			switch {
			case e == nil:
				row.Status = "bad"
			default:
				row.LastBackupAt, row.Proof = strp(ts(e.CreatedAt)), e.ProofLevel
				if now.Sub(e.CreatedAt) > time.Duration(staleHours)*time.Hour || len(e.Incomplete) > 0 || row.Plan == nil {
					row.Status = "warn"
				}
			}
			out.Workspaces = append(out.Workspaces, row)
		}
	}

	var users int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&users)
	summary := fmt.Sprintf("%s, %s, instance settings", plural(len(all), "workspace", "workspaces"), plural(users, "user", "users"))
	out.InstanceSummary = &summary
	return out, nil
}

func strp(s string) *string { return &s }

func joinDetail(cur *string, add string) string {
	if cur == nil || *cur == "" {
		return add
	}
	return *cur + " · " + add
}

// buildNights: fourteen nights ending today (in loc), oldest first. A night
// is ok when a run (or a bundle made by hand) landed on it, incomplete when
// any of that night's bundles recorded gaps (it was created but does not hold
// everything, so it is never drawn as a green ok), late when it was a
// catch-up, failed or skipped from the runs, none otherwise. proof is the
// best proof level among that night's bundles.
func buildNights(runs []*Run, bundles []backup.CatalogEntry, now time.Time, loc *time.Location) []Night {
	type agg struct {
		ok, late, failed, skipped bool
		proof                     int
		detail                    string
		incomplete                bool
		gaps                      []backup.IncompleteItem
	}
	days := map[string]*agg{}
	get := func(t time.Time) *agg {
		d := t.In(loc).Format("2006-01-02")
		if days[d] == nil {
			days[d] = &agg{}
		}
		return days[d]
	}
	proofByPath := map[string]int{}
	gapsByPath := map[string]bool{}
	for _, b := range bundles {
		proofByPath[b.FilePath] = b.ProofLevel
		a := get(b.CreatedAt)
		a.ok = true
		gapsByPath[b.FilePath] = len(b.Incomplete) > 0
		if len(b.Incomplete) > 0 {
			a.gaps = append(a.gaps, b.Incomplete...)
			a.incomplete = true
		}
		if b.ProofLevel > a.proof {
			a.proof = b.ProofLevel
		}
	}
	for _, r := range runs {
		at := r.StartedAt
		if r.DueAt != nil {
			at = *r.DueAt
		}
		a := get(at)
		switch r.Status {
		case StatusDone, StatusIncomplete:
			if r.Trigger == TriggerCatchup {
				a.late = true
				a.detail = "catch-up after a missed night"
			}
			if p := proofByPath[r.BundlePath]; p > a.proof {
				a.proof = p
			}
			// A run's gaps count once: the catalog row of its bundle
			// already added them.
			if r.Status == StatusIncomplete && (r.BundlePath == "" || !gapsByPath[r.BundlePath]) {
				a.gaps = append(a.gaps, r.Incomplete...)
				a.incomplete = true
			}
			a.ok = true
		case StatusFailed:
			a.failed = true
			if a.detail == "" {
				a.detail = r.Error
			}
		case StatusInterrupted:
			if r.RetriedAt == nil {
				a.failed = true
				if a.detail == "" {
					a.detail = r.Error
				}
			}
		case StatusSkipped:
			a.skipped = true
			if a.detail == "" {
				a.detail = r.Error
			}
		}
	}
	today := now.In(loc)
	end := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
	out := make([]Night, 0, 14)
	for i := 13; i >= 0; i-- {
		d := end.AddDate(0, 0, -i).Format("2006-01-02")
		n := Night{Date: d, Status: "none"}
		if a := days[d]; a != nil {
			switch {
			case a.failed && !a.ok:
				n.Status = "failed"
			case a.skipped && !a.ok:
				n.Status = "skipped"
			case a.incomplete:
				n.Status = "incomplete"
				gapDetail := "created · incomplete"
				if len(a.gaps) > 0 {
					gapDetail += ": " + describeIncomplete(a.gaps)
				}
				if a.late {
					gapDetail += " · catch-up after a missed night"
				}
				a.detail = gapDetail
			case a.late:
				n.Status = "late"
			case a.ok:
				n.Status = "ok"
			}
			if a.ok {
				n.Proof = a.proof
			}
			if a.detail != "" {
				n.Detail = strp(a.detail)
			}
		}
		out = append(out, n)
	}
	return out
}
