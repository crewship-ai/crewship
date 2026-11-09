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

// Producer run records: a producer (a script, routine or job calling the REST
// API or CLI with the same write authority as a data batch; agent runs on the
// artifact tool path cannot record runs yet) starts a run for one collection,
// writes its batches and finishes the run as succeeded or failed. Batches
// apply immediately: a run does not stage or atomically publish data, so
// readers may see values of a refresh that is still running or that failed.
//
// A run is bound to the collection's data generation (the sum of its document
// versions; documents are never deleted, so it only grows). Start records the
// current generation. Only the producer that started a run may finish it as
// succeeded, and only by naming the generation it wrote, which must still be
// current; a success over unchanged data must say so explicitly. Any writer
// may finish a run as failed (abandon it); the finisher is recorded. A failed
// run whose generation moved is partial. Run records never change data or
// code versions.
const (
	MaxDataRunReason     = 500
	MaxDataRunIdempotent = 128
)

// Defaults for DataOptions. They are server defaults, not API contract.
const (
	// DefaultDataRunsKept terminal runs per collection, plus the newest success.
	DefaultDataRunsKept = 20
	// DefaultDataRunsActive running runs per collection; more are refused.
	DefaultDataRunsActive = 4
	// DefaultDataRunKeyTTL after which a terminal run's idempotency key may be reused.
	DefaultDataRunKeyTTL = 24 * time.Hour
)

var (
	// ErrDataRunNotFound is a run id that this Page does not hold.
	ErrDataRunNotFound = errors.New("artifact data run not found")
	// ErrDataRunBusy refuses a new run while the collection has the maximum active runs.
	ErrDataRunBusy = errors.New("too many active runs for this collection")
	// ErrDataRunForbidden is a success claimed by a writer that did not start the run.
	ErrDataRunForbidden = errors.New("only the producer that started a run can finish it as succeeded")
	// ErrDataRunGeneration is a success that does not match the current data generation.
	ErrDataRunGeneration = errors.New("data generation is not current or does not match the claim")
)

type DataRun struct {
	ID              string `json:"id"`
	Collection      string `json:"collection"`
	State           string `json:"state"`
	StartedBy       string `json:"started_by"`
	StartedAt       string `json:"started_at"`
	StartGeneration int64  `json:"start_generation"`
	FinishedAt      string `json:"finished_at,omitempty"`
	FinishedBy      string `json:"finished_by,omitempty"`
	EndGeneration   *int64 `json:"end_generation,omitempty"`
	Reason          string `json:"failure_reason,omitempty"`
	Idempotency     string `json:"-"`
}

// Partial reports a failed run during which the collection's data changed.
func (r *DataRun) Partial() bool {
	return r.State == "failed" && r.EndGeneration != nil && *r.EndGeneration > r.StartGeneration
}

// DataRunFinish is how a producer ends a run. DataGeneration is required for a
// success and is the generation returned by the producer's last batch, or the
// run's start generation together with Unchanged when it verified that no
// data needed to change.
type DataRunFinish struct {
	State          string
	Reason         string
	DataGeneration *int64
	Unchanged      bool
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
// writers only, but it is stored, so common credential patterns never reach storage.
// Key names match inside identifiers (access_token, client_secret,
// PGPASSWORD), a closing quote may sit before the separator (JSON), "=>" and
// " is " count as separators, and a quoted value is taken whole so a space
// inside it does not leak the rest. URL userinfo is removed through the last
// "@" before the host. Known debt: lookalike characters and escaped quotes
// inside quoted values are not handled; these are common patterns, not a
// guarantee.
var reasonSecrets = []struct {
	pattern *regexp.Regexp
	replace string
}{
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`), "[redacted private key]"},
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`), "$1 [redacted]"},
	{regexp.MustCompile(`(?i)([A-Za-z0-9_-]*(?:token|password|passwd|pwd|secret|api[_-]?key|authorization|cookie|credential)[A-Za-z0-9_-]*["']?(?:\s*(?:=>|[=:])\s*|\s+is\s+))(?:"[^"]*"|'[^']*'|[^\s&,;"'}]+)`), "$1[redacted]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]*`), "[redacted]"},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), "[redacted]"},
	{regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`), "[redacted]"},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`), "[redacted]"},
	{regexp.MustCompile(`\bcrewship_[a-z]+_[A-Za-z0-9_]+`), "[redacted]"},
	{regexp.MustCompile(`\b(sk|pk|ghp|gho|ghs|xox[abp])[-_][A-Za-z0-9_-]{8,}`), "[redacted]"},
	{regexp.MustCompile(`://[^/\s]*@`), "://[redacted]@"},
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
	for _, secret := range reasonSecrets {
		reason = secret.pattern.ReplaceAllString(reason, secret.replace)
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

const dataRunColumns = `id,collection,state,started_by,started_at,start_generation,coalesce(finished_at,''),coalesce(finished_by,''),end_generation,coalesce(failure_reason,''),coalesce(idempotency_key,'')`

func scanDataRun(row interface{ Scan(...any) error }) (*DataRun, error) {
	var run DataRun
	var end sql.NullInt64
	if err := row.Scan(&run.ID, &run.Collection, &run.State, &run.StartedBy, &run.StartedAt, &run.StartGeneration, &run.FinishedAt, &run.FinishedBy, &end, &run.Reason, &run.Idempotency); err != nil {
		return nil, err
	}
	if end.Valid {
		run.EndGeneration = &end.Int64
	}
	return &run, nil
}

// DataGeneration is the collection's data generation inside tx.
func dataGeneration(ctx context.Context, tx *sql.Tx, key VersionKey, collection string) (int64, error) {
	var generation int64
	err := tx.QueryRowContext(ctx, `SELECT coalesce(sum(version),0) FROM artifact_data_documents WHERE workspace_id=? AND page_id=? AND collection=?`, key.WorkspaceID, key.PageID, collection).Scan(&generation)
	return generation, err
}

func (s *DataStore) runsKept() int {
	if s.opts.RunsKept > 0 {
		return s.opts.RunsKept
	}
	return DefaultDataRunsKept
}

func (s *DataStore) runsActive() int {
	if s.opts.RunsActive > 0 {
		return s.opts.RunsActive
	}
	return DefaultDataRunsActive
}

func (s *DataStore) runKeyTTL() time.Duration {
	if s.opts.RunKeyTTL > 0 {
		return s.opts.RunKeyTTL
	}
	return DefaultDataRunKeyTTL
}

// pruneDataRuns keeps the newest terminal runs and the newest success. A
// running run is never removed; admission bounds how many exist.
func (s *DataStore) pruneDataRuns(ctx context.Context, tx *sql.Tx, key VersionKey, collection string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM artifact_data_runs WHERE workspace_id=?1 AND page_id=?2 AND collection=?3 AND state!='running'
 AND id NOT IN (SELECT id FROM artifact_data_runs WHERE workspace_id=?1 AND page_id=?2 AND collection=?3 AND state!='running' ORDER BY finished_at DESC, rowid DESC LIMIT ?4)
 AND id NOT IN (SELECT id FROM artifact_data_runs WHERE workspace_id=?1 AND page_id=?2 AND collection=?3 AND state='succeeded' ORDER BY finished_at DESC, rowid DESC LIMIT 1)`, key.WorkspaceID, key.PageID, collection, s.runsKept())
	return err
}

func keyExpired(run *DataRun, ttl time.Duration, now time.Time) bool {
	if run.State == "running" {
		return false
	}
	finished, err := time.Parse(time.RFC3339Nano, run.FinishedAt)
	return err == nil && now.Sub(finished) > ttl
}

// StartRun records that a producer began refreshing collection. The same
// idempotency key from the same actor returns the existing run unchanged
// (created=false); from another actor it is a conflict. The key of a terminal
// run expires after the key TTL and then starts a new run. A collection with
// the maximum active runs refuses new ones with ErrDataRunBusy. The authority
// must reject a collection the current code version does not declare.
func (s *DataStore) StartRun(ctx context.Context, key VersionKey, collection, idempotency string, auth VersionAuthorize) (run *DataRun, created bool, err error) {
	if s == nil || s.db == nil || !s.opts.Enabled {
		return nil, false, ErrDisabled
	}
	if !dataIdentifier.MatchString(collection) || !idempotencyKey.MatchString(idempotency) {
		return nil, false, ErrDataInvalid
	}
	err = s.transaction(ctx, key, auth, func(tx *sql.Tx, actor string) error {
		now := time.Now().UTC()
		existing, err := scanDataRun(tx.QueryRowContext(ctx, `SELECT `+dataRunColumns+` FROM artifact_data_runs WHERE workspace_id=? AND page_id=? AND collection=? AND idempotency_key=?`, key.WorkspaceID, key.PageID, collection, idempotency))
		switch {
		case err == nil && keyExpired(existing, s.runKeyTTL(), now):
			if _, err := tx.ExecContext(ctx, `UPDATE artifact_data_runs SET idempotency_key=NULL WHERE workspace_id=? AND page_id=? AND id=? AND state!='running'`, key.WorkspaceID, key.PageID, existing.ID); err != nil {
				return err
			}
		case err == nil:
			if existing.StartedBy != actor {
				return ErrDataConflict
			}
			run = existing
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM artifact_data_runs WHERE workspace_id=? AND page_id=? AND collection=? AND state='running'`, key.WorkspaceID, key.PageID, collection).Scan(&active); err != nil {
			return err
		}
		if active >= s.runsActive() {
			return ErrDataRunBusy
		}
		if err := CheckStorageHeadroom(ctx, tx, 4096); err != nil {
			return err
		}
		generation, err := dataGeneration(ctx, tx, key, collection)
		if err != nil {
			return err
		}
		id, err := newDataRunID()
		if err != nil {
			return err
		}
		stamp := tsformat.Format(now)
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_data_runs(workspace_id,page_id,collection,id,idempotency_key,state,started_by,started_at,start_generation) VALUES(?,?,?,?,?,'running',?,?,?)`, key.WorkspaceID, key.PageID, collection, id, idempotency, actor, stamp, generation); err != nil {
			return err
		}
		if err := s.pruneDataRuns(ctx, tx, key, collection); err != nil {
			return err
		}
		run = &DataRun{ID: id, Collection: collection, State: "running", StartedBy: actor, StartedAt: stamp, StartGeneration: generation, Idempotency: idempotency}
		created = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return run, created, nil
}

// FinishRun ends a running run. A success needs the producer that started the
// run and a DataGeneration equal to the current generation; when that equals
// the start generation, Unchanged must be set, and Unchanged over changed data
// is refused. A failure may come from any writer and carries no generation.
// Finishing again with the same outcome returns the stored run; a different
// outcome is a conflict. A reason is only kept for a failure and is cleaned by
// CleanDataRunReason. The finisher and the end generation are recorded.
func (s *DataStore) FinishRun(ctx context.Context, key VersionKey, collection, id string, finish DataRunFinish, auth VersionAuthorize) (*DataRun, error) {
	if s == nil || s.db == nil || !s.opts.Enabled {
		return nil, ErrDisabled
	}
	state, reason := finish.State, finish.Reason
	if !dataIdentifier.MatchString(collection) || len(id) == 0 || len(id) > 64 || len(reason) > 4*MaxDataRunReason {
		return nil, ErrDataInvalid
	}
	switch state {
	case "succeeded":
		if reason != "" || finish.DataGeneration == nil || *finish.DataGeneration < 0 {
			return nil, ErrDataInvalid
		}
	case "failed":
		if finish.DataGeneration != nil || finish.Unchanged {
			return nil, ErrDataInvalid
		}
		reason = CleanDataRunReason(reason)
	default:
		return nil, ErrDataInvalid
	}
	var out *DataRun
	err := s.transaction(ctx, key, auth, func(tx *sql.Tx, actor string) error {
		run, err := scanDataRun(tx.QueryRowContext(ctx, `SELECT `+dataRunColumns+` FROM artifact_data_runs WHERE workspace_id=? AND page_id=? AND collection=? AND id=?`, key.WorkspaceID, key.PageID, collection, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDataRunNotFound
		}
		if err != nil {
			return err
		}
		if state == "succeeded" && run.StartedBy != actor {
			return ErrDataRunForbidden
		}
		if run.State != "running" {
			if run.State != state || run.Reason != reason || state == "succeeded" && (run.EndGeneration == nil || *run.EndGeneration != *finish.DataGeneration) {
				return ErrDataConflict
			}
			out = run
			return nil
		}
		current, err := dataGeneration(ctx, tx, key, collection)
		if err != nil {
			return err
		}
		if state == "succeeded" {
			claimed := *finish.DataGeneration
			if claimed != current || claimed < run.StartGeneration || (claimed == run.StartGeneration) != finish.Unchanged {
				return ErrDataRunGeneration
			}
		}
		stamp := tsformat.Format(time.Now().UTC())
		var stored any
		if reason != "" {
			stored = reason
		}
		if _, err := tx.ExecContext(ctx, `UPDATE artifact_data_runs SET state=?,finished_at=?,finished_by=?,end_generation=?,failure_reason=? WHERE workspace_id=? AND page_id=? AND collection=? AND id=? AND state='running'`, state, stamp, actor, current, stored, key.WorkspaceID, key.PageID, collection, id); err != nil {
			return err
		}
		if err := s.pruneDataRuns(ctx, tx, key, collection); err != nil {
			return err
		}
		run.State, run.FinishedAt, run.FinishedBy, run.EndGeneration, run.Reason = state, stamp, actor, &current, reason
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
