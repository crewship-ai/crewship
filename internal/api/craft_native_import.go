package api

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/craftapps"
	"github.com/crewship-ai/crewship/internal/craftengine"
	"github.com/crewship-ai/crewship/internal/craftsession"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Import of an xlsx workbook into a new, vacant native Sheet page.
//
// The browser uploads the file once. The server checks the archive, converts
// it with the document engine in a killable worker and keeps the native JSON
// for a short while; the status says when it is ready and what was not
// carried over. The client then publishes that JSON through the unchanged
// initialize route, so ownership, storage admission, validation and the
// edit lease are exactly those of a new document. Nothing here writes a
// version.

const (
	craftNativeImportMaxXLSX  = 64 << 20
	craftNativeImportKeep     = 15 * time.Minute
	craftNativeImportDeadline = 5 * time.Minute
	// The table holds at most this many imports, and one person this many: a new import of
	// theirs replaces their oldest finished one instead of taking room from others.
	craftNativeImportMaxJobs    = 16
	craftNativeImportMaxPerUser = 2
	// One workspace fills at most half of the table.
	craftNativeImportMaxPerWorkspace = craftNativeImportMaxJobs / 2
	// Conversions running at once (each engine may use 4 GiB), one per person.
	craftNativeImportMaxActive = 2
)

type craftNativeImportJob struct {
	id, workspace, page, user string
	created                   time.Time
	state                     string // converting, ready, failed
	code                      string
	archive                   *craftsession.ArchiveReport
	keptCells                 int64
	native                    []byte
	lost                      []craftsession.LostCells // what a refused workbook would have lost
}

type craftNativeImportStatusResponse struct {
	ID            string                      `json:"id"`
	State         string                      `json:"state"`
	Code          string                      `json:"code,omitempty"`
	Lost          []craftsession.LostCells    `json:"lost,omitempty"`
	Cells         *int64                      `json:"cells,omitempty"`
	DocumentBytes *int                        `json:"document_bytes,omitempty"`
	Archive       *craftsession.ArchiveReport `json:"archive,omitempty"`
}

type craftNativeImports struct {
	mu     sync.Mutex
	jobs   map[string]*craftNativeImportJob
	active int
	// receiving names the people whose workbook is being read or converted.
	receiving map[string]bool
	// keep replaces craftNativeImportKeep in tests.
	keep time.Duration

	moduleOnce sync.Once
	modulePath string
	moduleErr  error
	// convert replaces the engine conversion in tests.
	convert  func(ctx context.Context, data []byte) (*craftsession.ImportResult, error)
	tempRoot string
	maxXLSX  int64
	// export replaces the engine's xlsx export in tests (craft_native_export.go).
	export func(ctx context.Context, native []byte) ([]byte, error)
	// exportFaithful, when set, replaces the engine's fidelity-checked export (tests).
	exportFaithful func(ctx context.Context, native []byte) ([]byte, error)
	// exporting counts the exports in flight against craftNativeExportMaxActive.
	exporting int
}

func (d *craftNativeDocumentsHTTP) importer() *craftNativeImports {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.imports == nil {
		d.imports = &craftNativeImports{jobs: map[string]*craftNativeImportJob{}, receiving: map[string]bool{}}
	}
	return d.imports
}

// discard forgets a job and its document.
func (im *craftNativeImports) discard(id string) {
	im.mu.Lock()
	delete(im.jobs, id)
	im.mu.Unlock()
}

func (im *craftNativeImports) keepFor() time.Duration {
	if im.keep > 0 {
		return im.keep
	}
	return craftNativeImportKeep
}

func (im *craftNativeImports) sweepLocked(now time.Time) {
	for id, j := range im.jobs {
		if now.Sub(j.created) > im.keepFor() && j.state != "converting" {
			delete(im.jobs, id)
		}
	}
}

// admitLocked claims the person's conversion slot, or says why not. Their oldest finished
// imports make room for the new one.
func (im *craftNativeImports) admitLocked(user, workspace string) bool {
	if im.receiving[user] || im.active >= craftNativeImportMaxActive {
		return false
	}
	for {
		var own []*craftNativeImportJob
		for _, j := range im.jobs {
			if j.user == user && j.workspace == workspace {
				own = append(own, j)
			}
		}
		if len(own) < craftNativeImportMaxPerUser {
			break
		}
		var oldest *craftNativeImportJob
		for _, j := range own {
			if j.state != "converting" && (oldest == nil || j.created.Before(oldest.created)) {
				oldest = j
			}
		}
		if oldest == nil {
			return false
		}
		delete(im.jobs, oldest.id)
	}
	inWorkspace := 0
	for _, j := range im.jobs {
		if j.workspace == workspace {
			inWorkspace++
		}
	}
	if len(im.jobs) >= craftNativeImportMaxJobs || inWorkspace >= craftNativeImportMaxPerWorkspace {
		return false
	}
	im.receiving[user] = true
	im.active++
	return true
}

func (im *craftNativeImports) converter(p CraftNativeDocumentProfile) (func(context.Context, []byte) (*craftsession.ImportResult, error), error) {
	if im.convert != nil {
		return im.convert, nil
	}
	launch, err := im.launch(p)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, data []byte) (*craftsession.ImportResult, error) {
		return craftsession.ImportXLSX(ctx, launch, im.tempRoot, data, craftsession.ArchiveLimits{})
	}, nil
}

func (im *craftNativeImports) launch(p CraftNativeDocumentProfile) (craftsession.Launcher, error) {
	if p.EngineModule == nil || p.EngineModule.Load == nil || len(p.CLISHA256) != 64 {
		return nil, craftengine.ErrUnavailable
	}
	im.moduleOnce.Do(func() {
		module, err := p.EngineModule.Load()
		if err != nil {
			im.moduleErr = err
			return
		}
		dir, err := os.MkdirTemp(im.tempRoot, "crewship-craft-engine-")
		if err != nil {
			im.moduleErr = err
			return
		}
		im.modulePath = filepath.Join(dir, "engine.wasm")
		im.moduleErr = os.WriteFile(im.modulePath, module, 0o600)
	})
	if im.moduleErr != nil {
		return nil, craftengine.ErrUnavailable
	}
	return craftsession.ExecLauncher(p.Application, im.modulePath, p.CLISHA256, 4096), nil
}

func (d *craftNativeDocumentsHTTP) importStart(w http.ResponseWriter, r *http.Request) {
	if d.workbookSheets() {
		replyCode(w, 404, "use_initialize", "this server keeps Sheets as workbook files: start the Sheet from the file with craft/initialize")
		return
	}
	m := d.human
	if m == nil || !m.enabled || m.clock == nil || m.pages == nil {
		craftNativeImportRefused(w, "sheet_off", "Sheet is not available on this server")
		return
	}
	source, err := craftNativeHuman(r.Context())
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	profile, qualified := d.profile("gridcraft")
	if _, ok := d.application("gridcraft"); !qualified || !ok || !m.apps.allows(r.Context(), "gridcraft", craftapps.OpCreate) {
		craftNativeImportRefused(w, "sheet_off", "Sheet is not available on this server")
		return
	}
	ws := WorkspaceIDFromContext(r.Context())
	// Same authority as initialize, checked before the body is read: the
	// caller owns this page and it is still vacant.
	tx, err := m.pages.db.BeginTx(r.Context(), nil)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	rec, err := m.pages.loadPageForRead(ctx, ws, r.PathValue("slug"))
	if err == nil {
		_, err = m.initializeOwnerProof(ctx, tx, source, mooring.EditLeaseScope{WorkspaceID: ws, ArtifactID: rec.ID}, true)
	}
	_ = tx.Rollback()
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errCraftNativeAccess
		}
		craftNativeDocumentError(w, err)
		return
	}
	im := d.importer()
	im.mu.Lock()
	im.sweepLocked(time.Now())
	if !im.admitLocked(source.UserID, ws) {
		im.mu.Unlock()
		w.Header().Set("Retry-After", "30")
		craftNativeImportRefused(w, "busy", "too many workbook imports are running; try again shortly")
		return
	}
	im.mu.Unlock()
	release := func() { im.mu.Lock(); im.active--; delete(im.receiving, source.UserID); im.mu.Unlock() }
	limit := int64(craftNativeImportMaxXLSX)
	if im.maxXLSX > 0 {
		limit = im.maxXLSX
	}
	// The server's read timeout (15 s) is for ordinary requests: a 64 MiB workbook on a slow
	// link needs longer, bounded here to a minimum rate of about 1 MiB/s plus a minute.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(time.Minute + time.Duration(limit>>20)*time.Second))
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		release()
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			replyError(w, 413, "the workbook is larger than the import limit")
			return
		}
		replyError(w, 400, "the workbook could not be read")
		return
	}
	id, err := craftNativeOpaque()
	if err != nil {
		release()
		craftNativeDocumentError(w, err)
		return
	}
	convert, err := im.converter(profile)
	if err != nil {
		release()
		craftNativeDocumentError(w, mooring.ErrNativeDisabled)
		return
	}
	job := &craftNativeImportJob{id: id, workspace: ws, page: rec.ID, user: source.UserID, created: time.Now(), state: "converting"}
	im.mu.Lock()
	im.jobs[id] = job
	im.mu.Unlock()
	finishWork := beginBackgroundWork()
	go func() {
		defer finishWork()
		defer release()
		ctx, cancel := context.WithTimeout(context.Background(), craftNativeImportDeadline)
		defer cancel()
		res, err := convert(ctx, data)
		data = nil
		if err == nil && d.computed == nil {
			slog.Default().Info("Sheet import is not recorded as computed: no session manager is wired")
		}
		if err == nil && d.computed != nil {
			// The engine recalculated the workbook as it opened it: its next open keeps these values.
			d.computed(profile, res.Native)
		}
		im.mu.Lock()
		defer im.mu.Unlock()
		// The workbook is held for a while, not until the next request: an idle server forgets it too.
		time.AfterFunc(im.keepFor(), func() {
			im.mu.Lock()
			if im.jobs[id] == job {
				delete(im.jobs, id)
			}
			im.mu.Unlock()
		})
		switch {
		case err == nil:
			job.state, job.archive, job.keptCells, job.native = "ready", res.Archive, res.KeptCell, res.Native
		case errors.Is(err, craftsession.ErrNotXLSX):
			job.state, job.code = "failed", "not_xlsx"
		case errors.Is(err, craftsession.ErrTooLarge):
			job.state, job.code = "failed", "too_large"
		case errors.Is(err, craftsession.ErrTruncated):
			job.state, job.code = "failed", "truncated"
			var lost *craftsession.TruncatedError
			if errors.As(err, &lost) {
				job.lost = lost.Lost
			}
		default:
			job.state, job.code = "failed", "failed"
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "state": "converting"})
}

// craftNativeImportRefused answers 503 with a code the client acts on: "busy" (try again later,
// with Retry-After) or "sheet_off" (Sheet is not available on this server; trying again will not help).
func craftNativeImportRefused(w http.ResponseWriter, code, message string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": message, "code": code})
}

// importJob returns the caller's job, or nil. Another user, workspace or page
// gets the same answer as an unknown id. The id is no permission: every read
// proves again that the caller may initialize the page (owner, credential,
// membership); a job whose page they no longer may is forgotten.
func (d *craftNativeDocumentsHTTP) importJob(r *http.Request) *craftNativeImportJob {
	m := d.human
	if m == nil || !m.enabled || m.pages == nil {
		return nil
	}
	source, err := craftNativeHuman(r.Context())
	if err != nil {
		return nil
	}
	ws := WorkspaceIDFromContext(r.Context())
	rec, err := m.pages.loadPageForRead(r.Context(), ws, r.PathValue("slug"))
	if err != nil {
		return nil
	}
	im := d.importer()
	im.mu.Lock()
	im.sweepLocked(time.Now())
	j := im.jobs[r.PathValue("id")]
	im.mu.Unlock()
	if j == nil || j.user != source.UserID || j.workspace != ws || j.page != rec.ID {
		return nil
	}
	tx, err := m.pages.db.BeginTx(r.Context(), nil)
	if err != nil {
		return nil
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	_, err = m.initializeOwnerProof(ctx, tx, source, mooring.EditLeaseScope{WorkspaceID: ws, ArtifactID: rec.ID}, false)
	_ = tx.Rollback()
	if err != nil {
		im.mu.Lock()
		if im.jobs[j.id] == j {
			delete(im.jobs, j.id)
		}
		im.mu.Unlock()
		return nil
	}
	return j
}

func (d *craftNativeDocumentsHTTP) importStatus(w http.ResponseWriter, r *http.Request) {
	if d.workbookSheets() {
		replyCode(w, 404, "use_initialize", "this server keeps Sheets as workbook files: start the Sheet from the file with craft/initialize")
		return
	}
	j := d.importJob(r)
	if j == nil {
		replyError(w, 404, "import not found")
		return
	}
	im := d.importer()
	im.mu.Lock()
	out := craftNativeImportStatusResponse{ID: j.id, State: j.state, Code: j.code, Lost: j.lost}
	if j.state == "ready" {
		cells, size := j.keptCells, len(j.native)
		out.Cells, out.DocumentBytes, out.Archive = &cells, &size, j.archive
	}
	im.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

func (d *craftNativeDocumentsHTTP) importDocument(w http.ResponseWriter, r *http.Request) {
	if d.workbookSheets() {
		replyCode(w, 404, "use_initialize", "this server keeps Sheets as workbook files: start the Sheet from the file with craft/initialize")
		return
	}
	j := d.importJob(r)
	im := d.importer()
	if j == nil {
		replyError(w, 404, "import not found")
		return
	}
	im.mu.Lock()
	native, state := j.native, j.state
	im.mu.Unlock()
	if state != "ready" {
		replyError(w, 409, "the import is not ready")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(native)
}
