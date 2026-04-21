package episodic

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Importance is the score stored in journal_embeddings.importance_score.
// It multiplies cosine similarity at recall time so high-signal memories
// rise above same-cosine low-signal ones. Range [0, 1]; inputs outside
// that range are clamped. Deliberately simple — tune by adjusting
// BaseImportance weights, not by adding layers on top.
type Importance float64

// BaseImportance returns the seed importance for a freshly-indexed
// entry, derived from its type + severity + priority. The formula is
// intentionally flat — every factor is additive into [0, 1] — because a
// complicated score would obscure *why* a given memory ranks where it
// does during recall debugging.
//
// Reference points:
//   - a run-of-the-mill peer.conversation at severity=info: 0.5
//   - a keeper.decision that denied: 0.7
//   - an eval.regression_detected at severity=error: 0.95
//   - anything marked PriorityPermanent: clamped up to 0.95 minimum
//
// Inspired by OpenClaw Auto-Dream's `(base × recency × references) / 8.0`
// but adapted to Crewship's entry-type catalog and multi-tenant model:
// we compute base here, then DecayAndReinforce folds in recency +
// reference counts as a nightly update.
func BaseImportance(t journal.EntryType, sev journal.Severity, prio journal.Priority) Importance {
	score := 0.5

	// Per-type adjustment — signals that carried operator or regression
	// intent get a visible lift. peer.escalation is a direct "I blocked,
	// help" signal; summary.generated is the consolidator's curated
	// output; eval.regression_detected is a hard quality signal.
	switch t {
	case journal.EntryPeerEscalation:
		score += 0.2
	case journal.EntryPeerConversation:
		score += 0.0 // neutral — only escalated ones qualify to embed anyway
	case journal.EntrySummaryGenerated:
		score += 0.15
	case journal.EntryMemoryConsolidated:
		score += 0.2
	case journal.EntryApprovalDenied:
		score += 0.15
	case journal.EntryEvalRegression:
		score += 0.3
	case journal.EntryKeeperDecision:
		score += 0.1
	case journal.EntryMissionStatus:
		// Only warn/error level qualify to embed, so the additional
		// severity bump below will lift this naturally.
		score += 0.05
	}

	// Severity bump — warn/error always matter more than info/notice.
	switch sev {
	case journal.SeverityNotice:
		score += 0.05
	case journal.SeverityWarn:
		score += 0.15
	case journal.SeverityError:
		score += 0.25
	}

	// Priority override — explicit operator signal trumps the formula.
	// PriorityPermanent floors at 0.95, PriorityHigh at 0.85, PriorityPin
	// at 0.8. Normal priority is a no-op.
	switch prio {
	case journal.PriorityPermanent:
		if score < 0.95 {
			score = 0.95
		}
	case journal.PriorityHigh:
		if score < 0.85 {
			score = 0.85
		}
	case journal.PriorityPin:
		if score < 0.8 {
			score = 0.8
		}
	}

	return Importance(clamp01(score))
}

// RecencyFactor is the Auto-Dream decay — max(0.1, 1 - days/180).
// Six-month half-life is long enough that a rare-but-critical memory
// from last quarter still ranks, while fresh memories lead until they
// stop being referenced. 0.1 floor ensures we never multiply importance
// to literally zero — we still want the signal, just down-weighted.
func RecencyFactor(indexedAt time.Time, now time.Time) float64 {
	days := now.Sub(indexedAt).Hours() / 24
	if days < 0 {
		days = 0
	}
	return math.Max(0.1, 1.0-days/180.0)
}

// ReferenceBoost is the log-scaled lift from how often this memory has
// been recalled into agent prompts. Frequently-cited entries are, by
// the operator's actual usage pattern, important — so log2(refs+1)
// lifts them without letting a single runaway loop dominate.
func ReferenceBoost(refs int64) float64 {
	if refs < 0 {
		refs = 0
	}
	return math.Log2(float64(refs) + 1)
}

// DecayAndReinforce re-weights every row in journal_embeddings using
// the current recency + reference boost. Intended to run once per day
// (the consolidator runner calls it from StartBackground). The update
// is a single SQL UPDATE per row in a transaction; for typical
// journal_embeddings sizes (<100k rows per workspace) this finishes in
// under a second.
//
// The new score is:
//
//	importance_score =
//	    BASE(entry_type, severity, priority)
//	    * RecencyFactor(indexed_at, now)
//	    * (1 + ReferenceBoost(reference_count) / 8)
//
// Dividing the boost by 8 keeps a 100-reference entry from outranking
// a rare-but-critical one with zero refs — a common Auto-Dream footgun
// where frequency eats importance.
func DecayAndReinforce(ctx context.Context, db *sql.DB, now time.Time) (int, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT em.entry_id, em.indexed_at, em.reference_count,
		       e.entry_type, e.severity, e.priority
		  FROM journal_embeddings em
		  JOIN journal_entries e ON e.id = em.entry_id
		 WHERE em.dim > 0`)
	if err != nil {
		return 0, fmt.Errorf("episodic: decay query: %w", err)
	}
	defer rows.Close()

	type row struct {
		id        string
		indexedAt time.Time
		refs      int64
		etype     journal.EntryType
		sev       journal.Severity
		prio      journal.Priority
	}
	var pending []row
	for rows.Next() {
		var (
			r                          row
			indexedStr, sev, prio, kind string
			refs                       sql.NullInt64
		)
		if err := rows.Scan(&r.id, &indexedStr, &refs, &kind, &sev, &prio); err != nil {
			return 0, fmt.Errorf("episodic: decay scan: %w", err)
		}
		if t, perr := time.Parse(time.RFC3339Nano, indexedStr); perr == nil {
			r.indexedAt = t
		} else if t, perr := time.Parse("2006-01-02 15:04:05", indexedStr); perr == nil {
			r.indexedAt = t
		} else {
			// Unparseable timestamp → treat as fresh so we don't decay
			// a row we can't reason about.
			r.indexedAt = now
		}
		if refs.Valid {
			r.refs = refs.Int64
		}
		r.etype = journal.EntryType(kind)
		r.sev = journal.Severity(sev)
		r.prio = journal.Priority(prio)
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("episodic: decay iterate: %w", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("episodic: decay tx: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `UPDATE journal_embeddings SET importance_score = ? WHERE entry_id = ?`)
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("episodic: decay prep: %w", err)
	}
	defer stmt.Close()

	for _, r := range pending {
		base := float64(BaseImportance(r.etype, r.sev, r.prio))
		score := base * RecencyFactor(r.indexedAt, now) * (1 + ReferenceBoost(r.refs)/8)
		if _, err := stmt.ExecContext(ctx, clamp01(score), r.id); err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("episodic: decay update %s: %w", r.id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("episodic: decay commit: %w", err)
	}
	return len(pending), nil
}

// MarkReferenced is called by Recall for every hit that lands in a
// prompt. It increments reference_count and stamps last_referenced_at,
// which the next DecayAndReinforce run folds into the new score.
// Errors are logged by the caller — a missed reference stamp is not
// worth aborting a recall.
func MarkReferenced(ctx context.Context, db *sql.DB, entryIDs []string, now time.Time) error {
	if len(entryIDs) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("episodic: mark tx: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `UPDATE journal_embeddings
		SET reference_count = reference_count + 1,
		    last_referenced_at = ?
		WHERE entry_id = ?`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("episodic: mark prep: %w", err)
	}
	defer stmt.Close()
	stamp := now.UTC().Format(time.RFC3339Nano)
	for _, id := range entryIDs {
		if _, err := stmt.ExecContext(ctx, stamp, id); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("episodic: mark %s: %w", id, err)
		}
	}
	return tx.Commit()
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
