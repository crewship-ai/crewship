package artifacts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Producer run records: a producer (agent, routine or container script with
// the same write authority as a data batch) starts a run for one collection,
// writes its batches and finishes the run as succeeded or failed. Readers see
// whether a refresh is running, when one last ran and when one last
// succeeded. Run records never change data or code versions.
const (
	MaxDataRunsKept      = 20
	MaxDataRunReason     = 500
	MaxDataRunIdempotent = 128
)

// ErrDataRunNotFound is a run id that this Page does not hold.
var ErrDataRunNotFound = errors.New("artifact data run not found")

type DataRun struct {
	ID          string `json:"id"`
	Collection  string `json:"collection"`
	State       string `json:"state"`
	StartedBy   string `json:"started_by"`
	StartedAt   string `json:"started_at"`
	FinishedAt  string `json:"finished_at,omitempty"`
	Reason      string `json:"failure_reason,omitempty"`
	Idempotency string `json:"-"`
}

// DataRunSummary is the newest run of one collection with its last attempt
// and last success. LastSuccessAt is empty when no retained run succeeded.
type DataRunSummary struct {
	Collection    string   `json:"collection"`
	Latest        *DataRun `json:"latest"`
	LastAttemptAt string   `json:"last_attempt_at"`
	LastSuccessAt string   `json:"last_success_at,omitempty"`
}

var idempotencyKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// Secrets a producer might paste into an error message. The reason is shown to
// writers only, but it is stored, so obvious credentials never reach storage.
var reasonSecrets = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)\b(token|password|passwd|secret|api[_-]?key|authorization|cookie)(\s*[=:]\s*)\S+`),
	regexp.MustCompile(`\bcrewship_[a-z]+_[A-Za-z0-9_]+`),
	regexp.MustCompile(`\b(sk|pk|ghp|gho|ghs|xox[abp])[-_][A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`://[^/\s:@]+:[^/\s@]+@`),
}

// CleanDataRunReason bounds a failure reason to plain text: no control
// characters except newlines, invisible formatting removed, credentials
// redacted and at most MaxDataRunReason bytes on a rune boundary.
func CleanDataRunReason(reason string) string {
	if !utf8.ValidString(reason) {
		reason = strings.ToValidUTF8(reason, "?")
	}
	reason = strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, reason)
	for i, pattern := range reasonSecrets {
		switch i {
		case 0:
			reason = pattern.ReplaceAllString(reason, "$1 [redacted]")
		case 1:
			reason = pattern.ReplaceAllString(reason, "$1$2[redacted]")
		case 4:
			reason = pattern.ReplaceAllString(reason, "://[redacted]@")
		default:
			reason = pattern.ReplaceAllString(reason, "[redacted]")
		}
	}
	reason = strings.TrimSpace(reason)
	for len(reason) > MaxDataRunReason {
		_, size := utf8.DecodeLastRuneInString(reason)
		reason = reason[:len(reason)-size]
	}
	return reason
}

func newDataRunID() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "run_" + hex.EncodeToString(raw[:]), nil
}

const dataRunColumns = `id,collection,state,started_by,started_at,coalesce(finished_at,''),coalesce(failure_reason,''),idempotency_key`

func scanDataRun(row interface{ Scan(...any) error }) (*DataRun, error) {
	var run DataRun
	if err := row.Scan(&run.ID, &run.Collection, &run.State, &run.StartedBy, &run.StartedAt, &run.FinishedAt, &run.Reason, &run.Idempotency); err != nil {
		return nil, err
	}
	return &run, nil
}

// StartRun records that a producer began refreshing collection. The same
// idempotency key from the same actor returns the existing run unchanged
// (created=false); from another actor it is a conflict. The authority must
// reject a collection the current code version does not declare.
func (s *DataStore) StartRun(ctx context.Context, key VersionKey, collection, idempotency string, auth VersionAuthorize) (run *DataRun, created bool, err error) {
	if s == nil || s.db == nil || !s.opts.Enabled {
		return nil, false, ErrDisabled
	}
	if !dataIdentifier.MatchString(collection) || !idempotencyKey.MatchString(idempotency) {
		return nil, false, ErrDataInvalid
	}
	err = s.transaction(ctx, key, auth, func(tx *sql.Tx, actor string) error {
		existing, err := scanDataRun(tx.QueryRowContext(ctx, `SELECT `+dataRunColumns+` FROM artifact_data_runs WHERE workspace_id=? AND page_id=? AND collection=? AND idempotency_key=?`, key.WorkspaceID, key.PageID, collection, idempotency))
		if err == nil {
			if existing.StartedBy != actor {
				return ErrDataConflict
			}
			run = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := CheckStorageHeadroom(ctx, tx, 4096); err != nil {
			return err
		}
		id, err := newDataRunID()
		if err != nil {
			return err
		}
		stamp := tsformat.Format(time.Now().UTC())
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_data_runs(workspace_id,page_id,collection,id,idempotency_key,state,started_by,started_at) VALUES(?,?,?,?,?,'running',?,?)`, key.WorkspaceID, key.PageID, collection, id, idempotency, actor, stamp); err != nil {
			return err
		}
		// Bounded retention: the newest MaxDataRunsKept runs and the newest success stay.
		if _, err := tx.ExecContext(ctx, `DELETE FROM artifact_data_runs WHERE workspace_id=?1 AND page_id=?2 AND collection=?3
 AND id NOT IN (SELECT id FROM artifact_data_runs WHERE workspace_id=?1 AND page_id=?2 AND collection=?3 ORDER BY started_at DESC, rowid DESC LIMIT ?4)
 AND id NOT IN (SELECT id FROM artifact_data_runs WHERE workspace_id=?1 AND page_id=?2 AND collection=?3 AND state='succeeded' ORDER BY finished_at DESC, rowid DESC LIMIT 1)`, key.WorkspaceID, key.PageID, collection, MaxDataRunsKept); err != nil {
			return err
		}
		run = &DataRun{ID: id, Collection: collection, State: "running", StartedBy: actor, StartedAt: stamp, Idempotency: idempotency}
		created = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return run, created, nil
}

// FinishRun ends a running run as succeeded or failed. Finishing again with
// the same outcome returns the stored run; a different outcome is a conflict.
// A reason is only kept for a failure and is cleaned by CleanDataRunReason.
func (s *DataStore) FinishRun(ctx context.Context, key VersionKey, collection, id, state, reason string, auth VersionAuthorize) (*DataRun, error) {
	if s == nil || s.db == nil || !s.opts.Enabled {
		return nil, ErrDisabled
	}
	if !dataIdentifier.MatchString(collection) || len(id) == 0 || len(id) > 64 || (state != "succeeded" && state != "failed") || state == "succeeded" && reason != "" || len(reason) > 4*MaxDataRunReason {
		return nil, ErrDataInvalid
	}
	if state == "failed" {
		reason = CleanDataRunReason(reason)
	}
	var out *DataRun
	err := s.transaction(ctx, key, auth, func(tx *sql.Tx, _ string) error {
		run, err := scanDataRun(tx.QueryRowContext(ctx, `SELECT `+dataRunColumns+` FROM artifact_data_runs WHERE workspace_id=? AND page_id=? AND collection=? AND id=?`, key.WorkspaceID, key.PageID, collection, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDataRunNotFound
		}
		if err != nil {
			return err
		}
		if run.State != "running" {
			if run.State != state || run.Reason != reason {
				return ErrDataConflict
			}
			out = run
			return nil
		}
		stamp := tsformat.Format(time.Now().UTC())
		var stored any
		if reason != "" {
			stored = reason
		}
		if _, err := tx.ExecContext(ctx, `UPDATE artifact_data_runs SET state=?,finished_at=?,failure_reason=? WHERE workspace_id=? AND page_id=? AND id=? AND state='running'`, state, stamp, stored, key.WorkspaceID, key.PageID, id); err != nil {
			return err
		}
		run.State, run.FinishedAt, run.Reason = state, stamp, reason
		out = run
		return nil
	})
	return out, err
}

// RunSummaries returns, for each named collection, its newest run, last
// attempt and last success. Collections without runs are omitted. The
// authority selects which collections the caller may see.
func (s *DataStore) RunSummaries(ctx context.Context, key VersionKey, collections func() []string, auth VersionAuthorize) ([]DataRunSummary, error) {
	if s == nil || s.db == nil || !s.opts.Enabled {
		return nil, ErrDisabled
	}
	out := []DataRunSummary{}
	err := s.transaction(ctx, key, auth, func(tx *sql.Tx, _ string) error {
		out = out[:0]
		for _, collection := range collections() {
			latest, err := scanDataRun(tx.QueryRowContext(ctx, `SELECT `+dataRunColumns+` FROM artifact_data_runs WHERE workspace_id=? AND page_id=? AND collection=? ORDER BY started_at DESC, rowid DESC LIMIT 1`, key.WorkspaceID, key.PageID, collection))
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			summary := DataRunSummary{Collection: collection, Latest: latest, LastAttemptAt: latest.StartedAt}
			if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(finished_at),'') FROM artifact_data_runs WHERE workspace_id=? AND page_id=? AND collection=? AND state='succeeded'`, key.WorkspaceID, key.PageID, collection).Scan(&summary.LastSuccessAt); err != nil {
				return err
			}
			out = append(out, summary)
		}
		return nil
	})
	return out, err
}
