package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/backup/offsite"
	"github.com/crewship-ai/crewship/internal/backupplan"
	"github.com/crewship-ai/crewship/internal/encryption"
)

// memDest is an in-memory destination holding objects with their sha256.
type memDest struct {
	mu      sync.Mutex
	objects map[string][]byte
	// gate, when set, holds every Get until it is closed: a slow download.
	gate chan struct{}
}

func (d *memDest) put(key string, b []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.objects[key] = b
}
func (d *memDest) obj(key string, b []byte) offsite.Object {
	sum := sha256.Sum256(b)
	return offsite.Object{Key: key, Size: int64(len(b)), SHA256: hex.EncodeToString(sum[:]), Modified: time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)}
}
func (d *memDest) Kind() string { return "s3" }
func (d *memDest) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) (offsite.Object, error) {
	b, _ := io.ReadAll(r)
	d.put(key, b)
	return d.obj(key, b), nil
}
func (d *memDest) Get(ctx context.Context, key string) (io.ReadCloser, offsite.Object, error) {
	if d.gate != nil {
		select {
		case <-d.gate:
		case <-ctx.Done():
			return nil, offsite.Object{}, ctx.Err()
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.objects[key]
	if !ok {
		return nil, offsite.Object{}, offsite.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), d.obj(key, b), nil
}
func (d *memDest) List(_ context.Context, prefix string) ([]offsite.Object, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []offsite.Object
	for k, b := range d.objects {
		if strings.HasPrefix(k, prefix) {
			o := d.obj(k, b)
			o.SHA256 = ""
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
func (d *memDest) Head(_ context.Context, key string) (offsite.Object, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.objects[key]
	if !ok {
		return offsite.Object{}, offsite.ErrNotFound
	}
	return d.obj(key, b), nil
}
func (d *memDest) Delete(_ context.Context, key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.objects, key)
	return nil
}
func (d *memDest) Test(context.Context) error { return nil }

type copiesListBody struct {
	DestinationID string `json:"destination_id"`
	Copies        []struct {
		Key         string  `json:"key"`
		Size        int64   `json:"size"`
		Scope       string  `json:"scope"`
		WorkspaceID *string `json:"workspace_id"`
		Local       bool    `json:"local"`
		LocalPath   *string `json:"local_path"`
	} `json:"copies"`
}

type copyFetchBody struct {
	ID     string  `json:"id"`
	Status string  `json:"status"`
	Key    string  `json:"key"`
	Path   *string `json:"path"`
	Error  *string `json:"error"`
}

// Restore from an off-site copy: list what a destination holds, marking what
// is already on this server, and fetch one back as a job the caller follows,
// so a long download is never cut by a client's timeout.
func TestInstanceBackupCopiesListAndFetch(t *testing.T) {
	t.Setenv(encryption.KeyEnvVar("v1"), strings.Repeat("a7", 32))
	t.Setenv(encryption.KeyVersionEnvVar, "")
	dataDir := t.TempDir()
	t.Setenv("CREWSHIP_DATA_DIR", dataDir)
	f := newInstanceFixture(t)
	dest := &memDest{objects: map[string][]byte{}}
	prev := newDestinationClient
	newDestinationClient = func(offsite.S3Config) (offsite.Destination, error) { return dest, nil }
	t.Cleanup(func() { newDestinationClient = prev })
	rr := f.do(f.boss, "POST", "/api/v1/admin/instance/backups/destinations",
		`{"name":"r2","endpoint":"https://acct.r2.cloudflarestorage.com","region":"auto","bucket":"crewship-backups","access_key_id":"AKID","secret_access_key":"s3cr3t"}`)
	wantCode(t, rr, http.StatusCreated, "add destination")
	destID := decodeAs[struct {
		Destination backupplan.Destination `json:"destination"`
	}](t, rr.Body.Bytes()).Destination.ID

	// Transfers go to the in-memory destination; the record stays the database's.
	f.r.backupPlans.Destinations = func(ctx context.Context, id string) (offsite.Destination, *backupplan.Destination, error) {
		d, err := backupplan.GetDestination(ctx, f.db, id)
		if err != nil {
			return nil, nil, err
		}
		return dest, d, nil
	}

	const inst = "instance/crewship-instance-20260929T010000Z.tar.zst"
	const ws = "workspaces/ws-new/crewship-workspace-new-20260929T010000Z.tar.zst"
	dest.put(inst, []byte("instance bundle bytes"))
	dest.put(ws, []byte("workspace bundle bytes"))
	dest.put(ws+offsite.EnvironmentRefsSuffix, []byte(`{"blobs":[]}`))
	dest.put("environments/blobs/sha256/"+strings.Repeat("ab", 32), []byte("layer"))

	const base = "/api/v1/admin/instance/backups/copies"
	wantCode(t, f.do(f.boss, "GET", base, ""), http.StatusBadRequest, "list without a destination")
	wantCode(t, f.do(f.boss, "GET", base+"?destination=nope", ""), http.StatusNotFound, "list an unknown destination")
	wantCode(t, f.do(f.wsAdmin, "GET", base+"?destination="+destID, ""), http.StatusForbidden, "workspace ADMIN lists")
	wantCode(t, f.do(f.wsAdmin, "POST", base+"/fetch", `{"destination_id":"`+destID+`","key":"`+inst+`"}`), http.StatusForbidden, "workspace ADMIN fetches")

	rr = f.do(f.boss, "GET", base+"?destination="+destID, "")
	wantCode(t, rr, http.StatusOK, "list")
	list := decodeAs[copiesListBody](t, rr.Body.Bytes())
	if len(list.Copies) != 2 || list.Copies[0].Key != inst || list.Copies[0].Scope != "instance" || list.Copies[0].Local ||
		list.Copies[1].Key != ws || list.Copies[1].Scope != "workspace" || list.Copies[1].WorkspaceID == nil || *list.Copies[1].WorkspaceID != "ws-new" {
		t.Fatalf("list = %s", rr.Body.String())
	}

	for _, c := range []struct{ name, body string }{
		{"no key", `{"destination_id":"` + destID + `"}`},
		{"a refs object", `{"destination_id":"` + destID + `","key":"` + ws + offsite.EnvironmentRefsSuffix + `"}`},
		{"a layer", `{"destination_id":"` + destID + `","key":"environments/blobs/sha256/` + strings.Repeat("ab", 32) + `"}`},
		{"a path escape", `{"destination_id":"` + destID + `","key":"instance/../../etc/passwd"}`},
	} {
		wantCode(t, f.do(f.boss, "POST", base+"/fetch", c.body), http.StatusBadRequest, c.name)
	}
	wantCode(t, f.do(f.boss, "POST", base+"/fetch", `{"destination_id":"nope","key":"`+inst+`"}`), http.StatusNotFound, "unknown destination")
	wantCode(t, f.do(f.boss, "POST", base+"/fetch", `{"destination_id":"`+destID+`","key":"instance/missing.tar.zst"}`), http.StatusNotFound, "missing key")

	// A slow download: the request answers 202 at once and the job runs on.
	dest.gate = make(chan struct{})
	rr = f.do(f.boss, "POST", base+"/fetch", `{"destination_id":"`+destID+`","key":"`+inst+`"}`)
	wantCode(t, rr, http.StatusAccepted, "fetch")
	job := decodeAs[copyFetchBody](t, rr.Body.Bytes())
	if job.ID == "" || job.Status != "running" || job.Key != inst {
		t.Fatalf("fetch = %s", rr.Body.String())
	}
	// The same fetch again is the same job, not a second download.
	again := decodeAs[copyFetchBody](t, f.do(f.boss, "POST", base+"/fetch", `{"destination_id":"`+destID+`","key":"`+inst+`"}`).Body.Bytes())
	if again.ID != job.ID {
		t.Fatalf("a second fetch started job %s beside %s", again.ID, job.ID)
	}
	close(dest.gate)
	deadline := time.Now().Add(10 * time.Second)
	for job.Status == "running" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		rr = f.do(f.boss, "GET", base+"/fetch/"+job.ID, "")
		wantCode(t, rr, http.StatusOK, "fetch status")
		job = decodeAs[copyFetchBody](t, rr.Body.Bytes())
	}
	if job.Status != "done" || job.Path == nil {
		t.Fatalf("fetch ended = %+v", job)
	}
	dir, _ := backup.DefaultBackupsDir()
	if filepath.Dir(*job.Path) != dir {
		t.Fatalf("fetched into %s, want the backups dir %s", *job.Path, dir)
	}
	if b, err := os.ReadFile(*job.Path); err != nil || string(b) != "instance bundle bytes" {
		t.Fatalf("fetched bytes = %q, %v", b, err)
	}
	wantCode(t, f.do(f.boss, "GET", base+"/fetch/nope", ""), http.StatusNotFound, "unknown fetch")
	if !f.audited("instance.backup_copy_fetched") {
		t.Fatal("a fetch left no audit entry")
	}

	list = decodeAs[copiesListBody](t, f.do(f.boss, "GET", base+"?destination="+destID, "").Body.Bytes())
	if !list.Copies[0].Local || list.Copies[0].LocalPath == nil || *list.Copies[0].LocalPath != *job.Path || list.Copies[1].Local {
		t.Fatalf("after the fetch: %+v", list.Copies)
	}
	rr = f.do(f.boss, "POST", base+"/fetch", `{"destination_id":"`+destID+`","key":"`+inst+`"}`)
	wantCode(t, rr, http.StatusConflict, "fetch what is already here")
}
