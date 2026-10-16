package slidesexport

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
	"github.com/crewship-ai/crewship/internal/writerlease"
)

const maxIntentBytes = 4096
const maxCleanupAuditBytes = 1 << 20

var intentName = regexp.MustCompile(`^crewship-slide-export-[A-Za-z0-9]{20,64}$`)
var uncertainJobs sync.Map

type cleanupIntent struct {
	SchemaVersion int    `json:"schema_version"`
	Name          string `json:"name"`
	OwnerID       string `json:"owner_id"`
	Token         string `json:"token"`
	Phase         string `json:"phase"`
	ContainerID   string `json:"container_id"`
	CreatedAt     string `json:"created_at"`
}
type cleanupJournal struct {
	directory, owner string
	root             *os.Root
	identity         os.FileInfo
	lock             *writerlease.Lease
}

func openCleanupJournal(directory, owner string) (*cleanupJournal, error) {
	if directory == "" || !idPattern.MatchString(owner) {
		return nil, ErrCleanup
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, ErrCleanup
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		return nil, ErrCleanup
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrCleanup
	}
	lock, err := writerlease.Acquire(filepath.Join(directory, "journal.lock"))
	if err != nil {
		return nil, errors.Join(ErrCleanup, err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		_ = lock.Close()
		return nil, ErrCleanup
	}
	j := &cleanupJournal{directory: directory, owner: owner, root: root, identity: info, lock: lock}
	if err = j.verify(); err != nil {
		_ = j.close()
		return nil, err
	}
	return j, nil
}
func (j *cleanupJournal) verify() error {
	if j == nil || j.root == nil || j.lock.Verify() != nil {
		return ErrCleanup
	}
	named, err := os.Lstat(j.directory)
	if err != nil || !named.IsDir() || !os.SameFile(j.identity, named) {
		return ErrCleanup
	}
	return nil
}
func (j *cleanupJournal) close() error {
	if j == nil {
		return nil
	}
	return errors.Join(j.root.Close(), j.lock.Close())
}
func (j *cleanupJournal) sync() error {
	dir, err := j.root.Open(".")
	if err != nil {
		return ErrCleanup
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrCleanup
	}
	return nil
}
func validIntent(v cleanupIntent) bool {
	if v.SchemaVersion != 1 || !intentName.MatchString(v.Name) || !idPattern.MatchString(v.OwnerID) || !regexp.MustCompile(`^[A-Za-z0-9]{20,64}$`).MatchString(v.Token) || (v.Phase != "creating" && v.Phase != "created" && v.Phase != "uncertain") {
		return false
	}
	if v.Phase == "created" && !digestPattern.MatchString(v.ContainerID) {
		return false
	}
	if v.Phase != "created" && v.ContainerID != "" {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, v.CreatedAt)
	return err == nil
}
func (j *cleanupJournal) write(v cleanupIntent, initial bool) error {
	if j.verify() != nil || !validIntent(v) || v.OwnerID != j.owner {
		return ErrCleanup
	}
	data, err := json.Marshal(v)
	if err != nil || len(data) > maxIntentBytes {
		return ErrCleanup
	}
	target := v.Name + ".json"
	temporary := target
	if !initial {
		temporary = ".update-" + rand.Text()
	}
	f, err := j.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrCleanup
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return ErrCleanup
	}
	if !initial {
		if j.root.Rename(temporary, target) != nil {
			return ErrCleanup
		}
	}
	return j.sync()
}
func (j *cleanupJournal) read(name string) (cleanupIntent, error) {
	var v cleanupIntent
	if j.verify() != nil || !intentName.MatchString(name) {
		return v, ErrCleanup
	}
	info, err := j.root.Lstat(name + ".json")
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return v, ErrCleanup
	}
	f, err := j.root.Open(name + ".json")
	if err != nil {
		return v, ErrCleanup
	}
	defer f.Close()
	current, err := f.Stat()
	if err != nil || !os.SameFile(info, current) {
		return v, ErrCleanup
	}
	data, err := io.ReadAll(io.LimitReader(f, maxIntentBytes+1))
	if err != nil || len(data) > maxIntentBytes || strictJSON(data) != nil {
		return v, ErrCleanup
	}
	if _, err = exact(data, "schema_version", "name", "owner_id", "token", "phase", "container_id", "created_at"); err != nil {
		return v, ErrCleanup
	}
	if json.Unmarshal(data, &v) != nil || !validIntent(v) || v.Name != name || v.OwnerID != j.owner {
		return cleanupIntent{}, ErrCleanup
	}
	return v, nil
}
func (j *cleanupJournal) list() ([]cleanupIntent, error) {
	if j.verify() != nil {
		return nil, ErrCleanup
	}
	dir, err := j.root.Open(".")
	if err != nil {
		return nil, ErrCleanup
	}
	defer dir.Close()
	entries, err := dir.ReadDir(33)
	if err != nil && err != io.EOF {
		return nil, ErrCleanup
	}
	if len(entries) > 32 {
		return nil, ErrCleanup
	}
	result := []cleanupIntent{}
	for _, entry := range entries {
		name := entry.Name()
		if name == "journal.lock" || name == "audit.jsonl" {
			continue
		}
		if len(name) > 5 && name[len(name)-5:] == ".json" && intentName.MatchString(name[:len(name)-5]) {
			v, err := j.read(name[:len(name)-5])
			if err != nil {
				return nil, err
			}
			result = append(result, v)
			continue
		}
		return nil, ErrCleanup
	}
	return result, nil
}
func (j *cleanupJournal) remove(v cleanupIntent) error {
	current, err := j.read(v.Name)
	if err != nil || current.Token != v.Token || current.OwnerID != v.OwnerID {
		return ErrCleanup
	}
	if j.root.Remove(v.Name+".json") != nil {
		return ErrCleanup
	}
	if j.sync() != nil {
		return ErrCleanup
	}
	uncertainJobs.Delete(v.Name)
	return nil
}
func (j *cleanupJournal) audit(v cleanupIntent, reference string) error {
	if j.verify() != nil || !idPattern.MatchString(reference) {
		return ErrCleanup
	}
	event := map[string]any{"schema_version": 1, "kind": "operator_acknowledged_daemon_quiescent", "name": v.Name, "owner_id": v.OwnerID, "token_sha256": hash([]byte(v.Token)), "reference": reference, "local_uid": os.Geteuid(), "created_at": tsformat.Format(time.Now().UTC())}
	data, err := json.Marshal(event)
	if err != nil || len(data) > maxIntentBytes {
		return ErrCleanup
	}
	data = append(data, '\n')
	before, statErr := j.root.Lstat("audit.jsonl")
	if statErr != nil && !os.IsNotExist(statErr) {
		return ErrCleanup
	}
	if statErr == nil && (!before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0) {
		return ErrCleanup
	}
	f, err := j.root.OpenFile("audit.jsonl", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return ErrCleanup
	}
	defer f.Close()
	current, err := f.Stat()
	if err != nil || !current.Mode().IsRegular() || (statErr == nil && !os.SameFile(before, current)) || current.Size() > maxCleanupAuditBytes-int64(len(data)) {
		return ErrCleanup
	}
	if _, err = f.Write(data); err != nil {
		return ErrCleanup
	}
	if f.Sync() != nil {
		return ErrCleanup
	}
	return j.sync()
}
func quarantined() bool {
	found := false
	uncertainJobs.Range(func(_, _ any) bool { found = true; return false })
	return found
}
func newIntent(owner string) cleanupIntent {
	return cleanupIntent{SchemaVersion: 1, Name: "crewship-slide-export-" + rand.Text(), OwnerID: owner, Token: rand.Text(), Phase: "creating", CreatedAt: tsformat.Format(time.Now().UTC())}
}

// Reconcile is a trusted startup/maintenance operation under this journal's OS
// lease. It never resumes a job or infers that absent uncertain work is settled.
func (r *Runner) Reconcile(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	l, err := r.acquire(ctx, true)
	if err != nil {
		return err
	}
	defer l.Close()
	l.mu.Lock()
	defer l.mu.Unlock()
	intents, err := l.journal.list()
	if err != nil {
		return err
	}
	for _, v := range intents {
		if v.Phase == "created" {
			if err := r.reconcileOwned(ctx, v, false); err != nil {
				return err
			}
			if err := l.journal.remove(v); err != nil {
				return err
			}
		} else {
			uncertainJobs.Store(v.Name, true)
			_ = r.reconcileOwned(ctx, v, true)
			return ErrCleanup
		}
	}
	return nil
}

// AcknowledgeCleanup is local trusted maintenance only, never HTTP/agent/UI.
// The operator must independently establish daemon quiescence; this assertion
// is audited, not derived from a finite interval of container-name absence.
func (r *Runner) AcknowledgeCleanup(ctx context.Context, name, reference string, daemonQuiescent bool) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if !daemonQuiescent || !intentName.MatchString(name) || !idPattern.MatchString(reference) {
		return ErrCleanup
	}
	l, err := r.acquire(ctx, true)
	if err != nil {
		return err
	}
	defer l.Close()
	l.mu.Lock()
	defer l.mu.Unlock()
	v, err := l.journal.read(name)
	if err != nil || v.Phase == "created" {
		return ErrCleanup
	}
	if err = r.reconcileOwned(ctx, v, false); err != nil {
		return err
	}
	if err = l.journal.audit(v, reference); err != nil {
		return err
	}
	return l.journal.remove(v)
}

// Kept separate from ordinary report JSON: operational audit/intent never
// contains document data, principal credentials or a resumable capability.
