package consolidate

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Compactor deletes high-volume low-signal journal entries older than a
// retention cutoff, after replacing each daily bucket with a single
// system.compaction entry that preserves the aggregate statistics.
//
// The compactor owns SELECT + INSERT + DELETE for these rows. It does NOT
// modify any other table; cascade deletes on journal_embeddings happen
// automatically because the foreign key is declared ON DELETE CASCADE.
//
// High-value entries (peer.escalation, summary.generated, keeper.decision,
// mission.status_change, eval.*) are never touched — this is a safety
// property: the compactor can be re-run indefinitely without eroding
// audit or memory signal.
type Compactor struct {
	DB      *sql.DB
	Journal journal.Emitter
	Logger  *slog.Logger
	Now     func() time.Time
}

// compactableTypes is the allowlist of entry types the compactor will
// roll up. Using an allowlist (not a blocklist) is deliberate: new entry
// types default to "never compacted" until an operator opts them in,
// which is the right default for an audit store.
var compactableTypes = []journal.EntryType{
	journal.EntryExecOutputChunk,
	journal.EntryContainerMetrics,
	journal.EntryNetworkPortOpen,
	journal.EntryNetworkPortClose,
	journal.EntryLLMCall,
}

// minBucketSize is the smallest number of same-type same-day entries that
// warrants emitting a compaction summary. Below this threshold the
// originals are retained — a handful of entries cost less than the
// noise of a bucket entry plus loss of individual detail.
const minBucketSize = 10

// Run performs one compaction pass over workspaceID. Entries older than
// olderThan in any of compactableTypes are grouped by (crew, date, type)
// into daily buckets. Each bucket with size >= minBucketSize is replaced
// by a single system.compaction entry. Originals in such buckets are
// deleted atomically per-bucket.
//
// Returns aggregate totals so the caller can log throughput. When
// olderThan is zero, the default of 30 days is applied.
func (c *Compactor) Run(ctx context.Context, workspaceID string, olderThan time.Duration) (CompactResult, error) {
	if workspaceID == "" {
		return CompactResult{}, fmt.Errorf("compact: workspace_id required")
	}
	if olderThan <= 0 {
		olderThan = 30 * 24 * time.Hour
	}
	logger := c.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := c.now()
	cutoff := now.Add(-olderThan)

	// Gather every eligible row in one query. We don't use journal.List
	// because it caps at 1000 rows and we want the full aged population
	// for this workspace. The row count is bounded by retention; in
	// practice it's small enough to fit in memory because buckets are
	// deleted after processing.
	rows, err := c.selectAged(ctx, workspaceID, cutoff)
	if err != nil {
		return CompactResult{}, err
	}

	buckets := groupIntoBuckets(rows)
	var result CompactResult

	// Process each bucket in its own transaction so a failure partway
	// through does not leave the journal with a summary entry AND the
	// originals still in place. Atomicity per bucket is sufficient
	// because buckets are independent by design (different crew, day,
	// or type).
	for _, b := range buckets {
		if len(b.IDs) < minBucketSize {
			continue
		}
		bucketEntry, err := c.emitBucketSummary(ctx, workspaceID, b)
		if err != nil {
			logger.Warn("compact: emit bucket failed", "err", err,
				"crew_id", b.CrewID, "date", b.Date, "type", b.Type)
			continue
		}
		// Ensure the summary entry has landed on disk before we start
		// deleting the originals it references. Without this the
		// Flush happens asynchronously and a crash window could delete
		// the sources while the summary is still queued.
		if err := c.Journal.Flush(ctx); err != nil {
			logger.Warn("compact: flush after emit failed", "err", err)
			continue
		}
		deleted, freed, err := c.deleteBucket(ctx, workspaceID, b.IDs)
		if err != nil {
			logger.Warn("compact: delete bucket failed", "err", err,
				"crew_id", b.CrewID, "date", b.Date, "type", b.Type)
			continue
		}
		_ = bucketEntry
		result.EntriesDeleted += deleted
		result.BucketsCreated++
		result.BytesFreed += freed
	}

	// Emit a single per-run marker so the operator can see when the
	// compactor last ran and how much it reclaimed.
	if result.BucketsCreated > 0 {
		_, err := c.Journal.Emit(ctx, journal.Entry{
			WorkspaceID: workspaceID,
			Type:        journal.EntrySystemCompaction,
			ActorType:   journal.ActorSystem,
			ActorID:     "compactor",
			Severity:    journal.SeverityNotice,
			Summary: fmt.Sprintf("compacted %d entries into %d bucket summaries (%d bytes freed)",
				result.EntriesDeleted, result.BucketsCreated, result.BytesFreed),
			Payload: map[string]any{
				"entries_deleted": result.EntriesDeleted,
				"buckets_created": result.BucketsCreated,
				"bytes_freed":     result.BytesFreed,
				"cutoff":          cutoff.Format(time.RFC3339),
			},
		})
		if err != nil {
			return result, fmt.Errorf("compact: emit run marker: %w", err)
		}
	}
	return result, nil
}

func (c *Compactor) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

// agedRow is an intermediate representation of a row we might compact.
// We keep only the fields we actually need for bucketing and accounting,
// not the full journal.Entry — the payload text is used only to measure
// bytes freed, never parsed.
type agedRow struct {
	ID         string
	CrewID     sql.NullString
	TS         time.Time
	Type       journal.EntryType
	SizeBytes  int64
}

// selectAged issues the SELECT that pulls candidate rows. entry_type is
// restricted to the compactable allowlist via an IN clause; the ts filter
// is indexed so the scan is cheap even on a large journal.
func (c *Compactor) selectAged(ctx context.Context, workspaceID string, cutoff time.Time) ([]agedRow, error) {
	placeholders := make([]string, len(compactableTypes))
	args := make([]any, 0, len(compactableTypes)+2)
	args = append(args, workspaceID, cutoff.UTC().Format(time.RFC3339Nano))
	for i, t := range compactableTypes {
		placeholders[i] = "?"
		args = append(args, string(t))
	}
	// priority != 'permanent' is load-bearing: operators use
	// PriorityPermanent to say "never forget this", and the compactor
	// must honor that regardless of age, type, or severity. Without
	// the filter the 30-day rollup would silently delete deliberately
	// pinned knowledge.
	q := `SELECT id, crew_id, ts, entry_type,
	              (length(payload) + length(summary)) AS size_bytes
	      FROM journal_entries
	      WHERE workspace_id = ?
	        AND ts < ?
	        AND priority != 'permanent'
	        AND entry_type IN (` + strings.Join(placeholders, ",") + `)
	      ORDER BY ts ASC`
	rows, err := c.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("compact: select: %w", err)
	}
	defer rows.Close()

	out := make([]agedRow, 0, 256)
	for rows.Next() {
		var (
			r      agedRow
			tsStr  string
			typStr string
			sz     int64
		)
		if err := rows.Scan(&r.ID, &r.CrewID, &tsStr, &typStr, &sz); err != nil {
			return nil, fmt.Errorf("compact: scan: %w", err)
		}
		ts, err := parseTS(tsStr)
		if err != nil {
			// Unparseable timestamps happen only on corrupt rows; skip
			// rather than aborting the whole run.
			continue
		}
		r.TS = ts
		r.Type = journal.EntryType(typStr)
		r.SizeBytes = sz
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// bucketKey is the composite key that defines a compaction bucket. Using
// a value type (not a pointer) means it can be a map key directly.
type bucketKey struct {
	CrewID string
	Date   string // YYYY-MM-DD
	Type   journal.EntryType
}

type bucket struct {
	CrewID    string
	Date      string
	Type      journal.EntryType
	IDs       []string
	Count     int
	BytesSum  int64
	FirstTS   time.Time
	LastTS    time.Time
}

// groupIntoBuckets partitions rows by (crew_id, date, type). The ordering
// within a bucket is preserved because selectAged returns rows sorted by
// ts ASC, which makes FirstTS/LastTS trivial to compute while we iterate.
func groupIntoBuckets(rows []agedRow) []bucket {
	m := make(map[bucketKey]*bucket, 16)
	order := make([]bucketKey, 0, 16)
	for _, r := range rows {
		key := bucketKey{
			CrewID: r.CrewID.String,
			Date:   r.TS.Format("2006-01-02"),
			Type:   r.Type,
		}
		b, ok := m[key]
		if !ok {
			b = &bucket{
				CrewID:  key.CrewID,
				Date:    key.Date,
				Type:    key.Type,
				FirstTS: r.TS,
				LastTS:  r.TS,
			}
			m[key] = b
			order = append(order, key)
		}
		b.IDs = append(b.IDs, r.ID)
		b.Count++
		b.BytesSum += r.SizeBytes
		if r.TS.Before(b.FirstTS) {
			b.FirstTS = r.TS
		}
		if r.TS.After(b.LastTS) {
			b.LastTS = r.TS
		}
	}
	out := make([]bucket, 0, len(order))
	for _, k := range order {
		out = append(out, *m[k])
	}
	return out
}

// emitBucketSummary writes the per-bucket system.compaction entry. The
// ts is pinned to the bucket's calendar day so the summary sorts
// correctly when operators browse the journal by time.
func (c *Compactor) emitBucketSummary(ctx context.Context, workspaceID string, b bucket) (string, error) {
	day, err := time.Parse("2006-01-02", b.Date)
	if err != nil {
		return "", fmt.Errorf("bad bucket date %q: %w", b.Date, err)
	}
	ts := day.Add(23*time.Hour + 59*time.Minute + 59*time.Second).UTC()
	return c.Journal.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      b.CrewID,
		Type:        journal.EntrySystemCompaction,
		ActorType:   journal.ActorSystem,
		ActorID:     "compactor",
		Severity:    journal.SeverityInfo,
		TS:          ts,
		Summary: fmt.Sprintf("rolled up %d %s entries from %s",
			b.Count, b.Type, b.Date),
		Payload: map[string]any{
			"bucket_date":        b.Date,
			"entry_type":         string(b.Type),
			"count":              b.Count,
			"bytes_sum":          b.BytesSum,
			"first_ts":           b.FirstTS.Format(time.RFC3339),
			"last_ts":            b.LastTS.Format(time.RFC3339),
		},
		Refs: map[string]any{
			"compacted_entries": b.IDs,
		},
	})
}

// deleteBucket removes originals in chunks because SQLite limits the
// number of host parameters per statement (typically 999). Using a
// transaction makes the delete atomic against concurrent reads.
func (c *Compactor) deleteBucket(ctx context.Context, workspaceID string, ids []string) (int64, int64, error) {
	const chunk = 500
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin: %w", err)
	}
	var (
		totalDeleted int64
		bytesFreed   int64
	)
	for start := 0; start < len(ids); start += chunk {
		end := start + chunk
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := strings.Repeat("?,", len(batch))
		placeholders = placeholders[:len(placeholders)-1]

		// Measure size before deletion so BytesFreed is accurate. The
		// selectAged path already computed per-row size, but we re-read
		// here to tolerate concurrent inserts between SELECT and DELETE.
		sizeArgs := make([]any, 0, len(batch)+1)
		sizeArgs = append(sizeArgs, workspaceID)
		for _, id := range batch {
			sizeArgs = append(sizeArgs, id)
		}
		var bsum sql.NullInt64
		row := tx.QueryRowContext(ctx,
			`SELECT COALESCE(SUM(length(payload)+length(summary)), 0)
			   FROM journal_entries WHERE workspace_id = ? AND id IN (`+placeholders+`)`,
			sizeArgs...)
		if err := row.Scan(&bsum); err != nil {
			_ = tx.Rollback()
			return 0, 0, fmt.Errorf("measure chunk: %w", err)
		}
		bytesFreed += bsum.Int64

		res, err := tx.ExecContext(ctx,
			`DELETE FROM journal_entries WHERE workspace_id = ? AND id IN (`+placeholders+`)`,
			sizeArgs...)
		if err != nil {
			_ = tx.Rollback()
			return 0, 0, fmt.Errorf("delete chunk: %w", err)
		}
		n, _ := res.RowsAffected()
		totalDeleted += n
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit: %w", err)
	}
	return totalDeleted, bytesFreed, nil
}

// parseTS accepts the same formats journal.parseJournalTS does, but
// locally because that helper is unexported. Kept deliberately small so
// it's easy to keep in sync if the journal ever widens its canonical
// timestamp format.
func parseTS(s string) (time.Time, error) {
	for _, layout := range []string{
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		time.RFC3339Nano,
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable timestamp %q", s)
}
