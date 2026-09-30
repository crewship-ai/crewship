package api

// The seams between the backup service (internal/backupplan: plans, the
// scheduler, durable backup_runs) and the whole-instance backup (Track B:
// internal/backup instance bundles, internal/quiesce quiet window and holds,
// the recovery kit). One run system: every run — scheduled, catch-up or
// manual, instance or workspace — is a backup_runs row executed by the
// service; these adapters give it the instance-specific parts.
//
//	instanceExecutor  backupplan.Executor for scope=instance → backup.CreateInstanceBackup
//	backupQuiescer    backupplan.Quiescer: drains and opens the quiet window for an instance run
//	holdsPauser       backupplan.Pauser: scheduled runs are skipped while schedules are held
//	vaultKitInfo      backupplan.KitInfo: vault key versions the latest instance bundle cannot unlock

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/backupplan"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/quiesce"
)

// instanceDrainWait bounds the quiet window's own drain. The service has
// already waited out the plan's busy window before it gets here, so this only
// covers work that started since; past it the run goes back to the busy
// window (ErrInstanceBusy) instead of blocking a run slot for two hours.
const instanceDrainWait = 2 * time.Minute

func (h *InstanceBackupsHandler) quiesceController() *quiesce.Controller {
	if c := h.recoveryConfig().Quiesce; c != nil {
		return c
	}
	return quiesce.Default()
}

func (h *InstanceBackupsHandler) putWindow(runID string, w *quiesce.Window) {
	h.windowsMu.Lock()
	defer h.windowsMu.Unlock()
	if h.windows == nil {
		h.windows = map[string]*quiesce.Window{}
	}
	h.windows[runID] = w
}

func (h *InstanceBackupsHandler) takeWindow(runID string) *quiesce.Window {
	h.windowsMu.Lock()
	defer h.windowsMu.Unlock()
	w := h.windows[runID]
	delete(h.windows, runID)
	return w
}

// instanceExecutor writes an instance bundle for a backup_runs row: the whole
// database, every file store and every crew container, copied inside the
// quiet window the quiescer opened; packing and sealing run after it is
// released. Vault keys ride along only when the recovery kit is on.
type instanceExecutor struct {
	h  *InstanceBackupsHandler
	db *sql.DB
}

func (e *instanceExecutor) Run(ctx context.Context, spec backupplan.RunSpec, progress func(string)) (*backupplan.RunResult, error) {
	window := e.h.takeWindow(spec.RunID)
	release := func() {
		if window != nil {
			window.Release()
		}
	}
	kit, err := backup.RecoveryKitEnabled(ctx, e.db)
	if err != nil {
		release()
		return nil, err
	}
	cfg := e.h.recoveryConfig()
	res, err := backup.CreateInstanceBackup(ctx, e.db, backup.InstanceOptions{
		OutputDir: cfg.OutputDir, CrewshipVersion: cfg.CrewshipVersion, Actor: spec.Actor, Level: spec.ScopeLevel(),
		Passphrase: spec.Passphrase, Recipients: spec.Recipients, Paths: cfg.Paths, RecoveryKit: kit,
		DockerOps: cfg.DockerOps, CrewContainerName: cfg.CrewContainerName,
		Quiesce: e.h.quiesceController(), HoldCap: spec.HoldCap, BusyWait: instanceDrainWait,
		Window: window, Progress: progress,
		EnvMode: spec.EnvMode(), EnvInline: backup.EnvironmentInlineDefault(),
		EncoderConcurrency: spec.EncoderConcurrency, DiskThrottle: spec.DiskThrottle,
	})
	if err != nil {
		release()
		return nil, err
	}
	hold := res.HoldMS
	return &backupplan.RunResult{Path: res.Path, Size: res.Size, SHA256: res.SHA256, Manifest: res.Manifest, HoldMS: &hold}, nil
}

// backupQuiescer opens the quiet window for an instance run: it drains
// running work (the same probe the instance backup uses) and then holds every
// write to protected data, never longer than the plan's hold cap. The window
// is handed to the run's executor, and released by the service as soon as the
// copy ends ("pack"), on error, or when the cap fires.
//
// Workspace runs open no instance-wide window: holding every write on the
// server for one workspace's copy would stall the others, and a workspace
// bundle already refuses to copy while that workspace's agents run
// (backup.ErrAgentRunning, which sends the run back to its busy window).
type backupQuiescer struct {
	h  *InstanceBackupsHandler
	db *sql.DB
}

func (q backupQuiescer) Quiesce(ctx context.Context, spec backupplan.RunSpec, capDur time.Duration) (func(), error) {
	if spec.Scope != backupplan.ScopeInstance {
		return func() {}, nil
	}
	w, err := q.h.quiesceController().Begin(ctx, quiesce.Options{
		BusyWait: instanceDrainWait, HoldCap: capDur, Busy: backup.InstanceBusy(q.db), Reason: "instance backup",
	})
	if err != nil {
		if errors.Is(err, quiesce.ErrBusy) || errors.Is(err, quiesce.ErrAlreadyHeld) {
			return nil, fmt.Errorf("%w: %v", backup.ErrInstanceBusy, err)
		}
		return nil, err
	}
	q.h.putWindow(spec.RunID, w)
	return func() {
		w.Release()
		q.h.takeWindow(spec.RunID)
	}, nil
}

// holdsPauser: while an instance restore's "routines" hold (routines and
// schedules) or "all" is set, scheduled and catch-up backup runs are recorded
// as skipped with the reason instead of starting. A restored server is under
// review until an admin resumes it, and a scheduled run then would rotate
// good pre-restore bundles away in favour of copies of a state nobody has
// checked. Manual runs still start: an admin who asks for a backup of the
// restored server gets one. The holds are read from instance_holds, the
// table `holds resume` clears, so a resume takes effect on the next tick.
type holdsPauser struct{ db *sql.DB }

func (p holdsPauser) Paused(ctx context.Context) (bool, string) {
	holds, err := quiesce.ReadHolds(ctx, p.db)
	if err != nil {
		return false, ""
	}
	for _, h := range holds {
		if h.Key == quiesce.HoldRoutines || h.Key == quiesce.HoldAll {
			return true, fmt.Sprintf("an instance restore left schedules held; resume with: crewship admin instance holds resume %s", h.Key)
		}
	}
	return false, ""
}

// vaultKitInfo reads the manifest of the latest instance bundle: every key
// version its sealed values use that its recovery kit does not carry (all of
// them when the kit was off) is one a new server cannot unlock.
type vaultKitInfo struct{ db *sql.DB }

func (k vaultKitInfo) MissingVaultKeys(ctx context.Context) ([]string, int, error) {
	entries, err := backup.ListCatalog(ctx, k.db, "")
	if err != nil {
		return nil, 0, err
	}
	var latest *backup.CatalogEntry
	for i := range entries {
		e := &entries[i]
		if e.Scope != string(backup.ScopeInstance) || e.Kind == backup.KindCustom {
			continue
		}
		if latest == nil || e.CreatedAt.After(latest.CreatedAt) {
			latest = e
		}
	}
	if latest == nil {
		return nil, 0, nil
	}
	m, err := backup.Inspect(ctx, latest.FilePath)
	if err != nil {
		return nil, 0, err
	}
	inst := m.Contents.Instance
	if inst == nil {
		return nil, 0, nil
	}
	inKit := map[string]bool{}
	for _, v := range inst.KitVersions {
		inKit[v] = true
	}
	var missing []string
	sealed := 0
	for v, n := range inst.VaultEnvelopes {
		if n > 0 && !inKit[v] {
			missing = append(missing, v)
			sealed += n
		}
	}
	encryption.SortVersions(missing)
	return missing, sealed, nil
}

// GetRun is GET /api/v1/admin/instance/backups/run/{runId}: one run, the
// same shape as an item of GET …/backups/runs.
func (p *InstanceBackupPlansHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	v, err := backupplan.GetRunView(r.Context(), p.h.db, p.svc.Recipients, r.PathValue("runId"))
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, backupplan.ErrNotFound) {
		replyError(w, http.StatusNotFound, "no such run")
		return
	}
	if err != nil {
		p.h.fail(w, "get run", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
