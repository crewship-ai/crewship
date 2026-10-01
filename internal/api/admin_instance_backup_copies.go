package api

// Restore from an off-site copy. When this server lost its bundles (or a new
// server is being brought up from a destination), an instance admin lists
// what a destination holds and fetches one bundle back — with the environment
// layers its refs object names — into this server's backups directory. It is
// then an ordinary local bundle for Recovery: a workspace restore, a drill,
// or `crewship recover` offline.
//
//	GET  /api/v1/admin/instance/backups/copies?destination=<id>   the bundles at a destination, marking those already here
//	POST /api/v1/admin/instance/backups/copies/fetch              { destination_id, key } → 202 and a fetch job
//	GET  /api/v1/admin/instance/backups/copies/fetch/{id}         the job: running, done (with the local path) or failed
//
// A fetch can move gigabytes, far longer than a client waits on one request
// (the CLI gives up after 30 s). So it runs as a job with its own deadline,
// detached from the request that started it; callers follow it by id. Jobs
// live in this process: a restart forgets them, and a half-downloaded bundle
// never becomes visible (offsite.Download writes to a temp file and renames).

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/backup/offsite"
	"github.com/crewship-ai/crewship/internal/backupplan"
)

// offsiteFetchDeadline bounds one fetch: generous for a large bundle over a
// slow link, but not forever.
const offsiteFetchDeadline = 6 * time.Hour

// offsiteFetchJobsKept bounds how many finished jobs are remembered.
const offsiteFetchJobsKept = 50

type offsiteCopiesHandler struct {
	p *InstanceBackupPlansHandler
	// fetch is backupplan.FetchOffsiteCopy; a seam for tests.
	fetch func(ctx context.Context, db *sql.DB, open backupplan.DestinationOpener, destinationID, key, dir string) (string, offsite.BundleTransfer, error)

	mu   sync.Mutex
	jobs map[string]*offsiteFetchJob
	// order is job ids oldest first, for trimming finished ones.
	order []string
}

func newOffsiteCopiesHandler(p *InstanceBackupPlansHandler) *offsiteCopiesHandler {
	return &offsiteCopiesHandler{p: p, fetch: backupplan.FetchOffsiteCopy, jobs: map[string]*offsiteFetchJob{}}
}

type offsiteCopyRow struct {
	Key      string    `json:"key"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	// Scope is "instance" or "workspace", read from the key's layout
	// (backupplan.ObjectKey).
	Scope       string  `json:"scope"`
	WorkspaceID *string `json:"workspace_id"`
	// Local is true when this bundle is already on this server; LocalPath
	// is where.
	Local     bool    `json:"local"`
	LocalPath *string `json:"local_path"`
}

type offsiteCopyList struct {
	DestinationID   string           `json:"destination_id"`
	DestinationName string           `json:"destination_name"`
	Copies          []offsiteCopyRow `json:"copies"`
}

type offsiteFetchJob struct {
	ID            string     `json:"id"`
	DestinationID string     `json:"destination_id"`
	Key           string     `json:"key"`
	Status        string     `json:"status"` // running | done | failed
	Path          *string    `json:"path"`
	Size          int64      `json:"size"`
	Layers        int        `json:"layers"`
	Error         *string    `json:"error"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at"`
}

func (c *offsiteCopiesHandler) opener() backupplan.DestinationOpener {
	if c.p.svc.Destinations != nil {
		return c.p.svc.Destinations
	}
	return backupplan.DBDestinations(c.p.h.db)
}

// destination looks the id up, answering 404 itself when there is none.
func (c *offsiteCopiesHandler) destination(w http.ResponseWriter, r *http.Request, id string) (offsite.Destination, *backupplan.Destination, bool) {
	if _, err := backupplan.GetDestination(r.Context(), c.p.h.db, id); err != nil {
		if errors.Is(err, backupplan.ErrNotFound) {
			replyError(w, http.StatusNotFound, "no backup destination with that id")
			return nil, nil, false
		}
		c.p.h.fail(w, "get destination", err)
		return nil, nil, false
	}
	dst, d, err := c.opener()(r.Context(), id)
	if err != nil {
		replyError(w, http.StatusBadGateway, "the destination could not be opened: "+err.Error())
		return nil, nil, false
	}
	return dst, d, true
}

// isBundleKey reports whether key names a bundle: not a refs object, not an
// environment layer, not the connection probe, and nothing a path could
// escape with.
func isBundleKey(key string) bool {
	if key == "" || strings.HasSuffix(key, "/") || strings.HasPrefix(key, "environments/") || strings.HasPrefix(key, ".") ||
		strings.HasSuffix(key, offsite.EnvironmentRefsSuffix) || strings.Contains(key, "..") || strings.ContainsAny(key, "\\\x00\r\n") {
		return false
	}
	return offsite.ValidateKey(key) == nil
}

func copyScope(key string) (string, *string) {
	if rest, ok := strings.CutPrefix(key, "workspaces/"); ok {
		if i := strings.Index(rest, "/"); i > 0 {
			ws := rest[:i]
			return "workspace", &ws
		}
	}
	return "instance", nil
}

// localCopy is where this server already holds the bundle at key, if it does:
// a recorded copy whose bundle is still on disk, or a file of that name in
// the backups directory.
func (c *offsiteCopiesHandler) localCopy(ctx context.Context, destinationID, key, dir string) string {
	var p string
	if err := c.p.h.db.QueryRowContext(ctx, `SELECT bundle_path FROM backup_copies WHERE destination_id = ? AND object_key = ? LIMIT 1`, destinationID, key).Scan(&p); err == nil && p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if dir != "" {
		cand := filepath.Join(dir, filepath.Base(key))
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return ""
}

// List is GET /api/v1/admin/instance/backups/copies?destination=<id>.
func (c *offsiteCopiesHandler) List(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("destination"))
	if id == "" {
		replyError(w, http.StatusBadRequest, "name the destination: ?destination=<id>")
		return
	}
	dst, d, ok := c.destination(w, r, id)
	if !ok {
		return
	}
	objs, err := dst.List(r.Context(), "")
	if err != nil {
		replyError(w, http.StatusBadGateway, "the destination could not be listed: "+err.Error())
		return
	}
	dir, _ := c.p.backupsDir()
	out := offsiteCopyList{DestinationID: id, Copies: []offsiteCopyRow{}}
	if d != nil {
		out.DestinationName = d.Name
	}
	for _, o := range objs {
		if !isBundleKey(o.Key) {
			continue
		}
		row := offsiteCopyRow{Key: o.Key, Size: o.Size, Modified: o.Modified}
		row.Scope, row.WorkspaceID = copyScope(o.Key)
		if p := c.localCopy(r.Context(), id, o.Key, dir); p != "" {
			row.Local, row.LocalPath = true, &p
		}
		out.Copies = append(out.Copies, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// Fetch is POST /api/v1/admin/instance/backups/copies/fetch:
//
//	{ "destination_id": "…", "key": "instance/crewship-instance-….tar.zst" }
//
// It answers 202 with the job at once; the download runs on. The same
// destination and key while a fetch of it runs is that job again. A bundle
// already on this server is a 409 naming where.
func (c *offsiteCopiesHandler) Fetch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DestinationID string `json:"destination_id"`
		Key           string `json:"key"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.DestinationID, body.Key = strings.TrimSpace(body.DestinationID), strings.TrimSpace(body.Key)
	if body.DestinationID == "" {
		replyError(w, http.StatusBadRequest, "destination_id is required")
		return
	}
	if !isBundleKey(body.Key) {
		replyError(w, http.StatusBadRequest, "key must name a bundle at the destination (see GET /api/v1/admin/instance/backups/copies)")
		return
	}
	dst, _, ok := c.destination(w, r, body.DestinationID)
	if !ok {
		return
	}
	c.mu.Lock()
	for _, j := range c.jobs {
		if j.Status == "running" && j.DestinationID == body.DestinationID && j.Key == body.Key {
			cp := *j
			c.mu.Unlock()
			writeJSON(w, http.StatusAccepted, cp)
			return
		}
	}
	c.mu.Unlock()
	dir, err := c.p.backupsDir()
	if err != nil {
		c.p.h.fail(w, "backups dir", err)
		return
	}
	if p := c.localCopy(r.Context(), body.DestinationID, body.Key, dir); p != "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "this bundle is already on this server; restore it from here", "local_path": p})
		return
	}
	headCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	_, err = dst.Head(headCtx, body.Key)
	cancel()
	if errors.Is(err, offsite.ErrNotFound) {
		replyError(w, http.StatusNotFound, "the destination holds no object with that key")
		return
	}
	if err != nil {
		replyError(w, http.StatusBadGateway, "the destination could not be reached: "+err.Error())
		return
	}

	job := &offsiteFetchJob{ID: generateCUID(), DestinationID: body.DestinationID, Key: body.Key, Status: "running", StartedAt: time.Now().UTC()}
	c.mu.Lock()
	c.jobs[job.ID] = job
	c.order = append(c.order, job.ID)
	c.trimLocked()
	snapshot := *job
	c.mu.Unlock()
	if err := auditInstance(r.Context(), r, c.p.h.db, "instance.backup_copy_fetched", "backup_copy", job.ID, "", map[string]any{
		"destination_id": body.DestinationID, "key": body.Key,
	}); err != nil {
		c.p.h.logger.Warn("instance backups: audit copy fetch", "error", err)
	}

	// Detached from the request (which ends now) but not unbounded.
	ctx, stop := context.WithTimeout(context.WithoutCancel(r.Context()), offsiteFetchDeadline)
	open := c.opener()
	finish := beginBackgroundWork()
	go func() {
		defer finish()
		defer stop()
		path, tr, err := c.fetch(ctx, c.p.h.db, open, job.DestinationID, job.Key, dir)
		now := time.Now().UTC()
		c.mu.Lock()
		defer c.mu.Unlock()
		job.EndedAt = &now
		job.Size = tr.Bundle.Size
		job.Layers = len(tr.Blobs)
		if err != nil {
			msg := err.Error()
			job.Status, job.Error = "failed", &msg
			c.p.h.logger.Warn("instance backups: off-site fetch failed", "destination", job.DestinationID, "key", job.Key, "error", err)
			return
		}
		job.Status, job.Path = "done", &path
	}()
	writeJSON(w, http.StatusAccepted, snapshot)
}

// trimLocked forgets the oldest finished jobs beyond offsiteFetchJobsKept.
func (c *offsiteCopiesHandler) trimLocked() {
	for len(c.order) > offsiteFetchJobsKept {
		dropped := false
		for i, id := range c.order {
			if j := c.jobs[id]; j == nil || j.Status != "running" {
				delete(c.jobs, id)
				c.order = append(c.order[:i], c.order[i+1:]...)
				dropped = true
				break
			}
		}
		if !dropped {
			return
		}
	}
}

// FetchStatus is GET /api/v1/admin/instance/backups/copies/fetch/{id}.
func (c *offsiteCopiesHandler) FetchStatus(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	j, ok := c.jobs[r.PathValue("id")]
	var cp offsiteFetchJob
	if ok {
		cp = *j
	}
	c.mu.Unlock()
	if !ok {
		replyError(w, http.StatusNotFound, "no off-site fetch with that id on this server (a restart forgets them)")
		return
	}
	writeJSON(w, http.StatusOK, cp)
}
