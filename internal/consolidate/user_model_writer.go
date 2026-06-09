package consolidate

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
)

// PR #10 F6 — UserModelSync auxiliary writer.
//
// Mirrors the PeerCardSync writer (peer_card_writer.go) but the unit
// of identity is the (user, workspace) pair ALONE — there is no
// agent_id. A peer card captures how an operator relates to ONE agent;
// a user model captures how an operator likes to work across the whole
// workspace, and every agent in a crew reads the same file.
//
// This package owns the deterministic plumbing:
//
//   - Threshold check (≥10 messages OR ≥5 minutes session)
//   - user_peer_consent gate (shared opt-out; opted-out users skipped
//     + purged — opting out of peer cards opts out of user models too)
//   - DB index upkeep (user_models INSERT / DELETE)
//   - Audit log emission (peer_card_audit per write / delete — reused
//     so a SAR "everything stored about user X" stays one index hit)
//   - On-disk persistence via memory.WriteUserModel / DeleteUserModel
//   - The MERGE step (MergeUserModel) that preserves prior high-
//     confidence fields when the new transcript is silent about them
//
// The aux-LLM extraction call lives in the routine handler
// (user_model_worker.go wires a UserModelExtractor); this package
// stays free of LLM client dependencies so the test surface is fast.

// UserModelThreshold is the minimum interaction signal required before
// a (user, workspace) pair gets a model written. Same OR semantics and
// defaults as the peer card threshold.
type UserModelThreshold struct {
	MinMessages int
	MinSession  time.Duration
}

// DefaultUserModelThreshold is the PRD-documented "≥10 messages OR ≥5
// minutes" bar, matching the peer card default.
var DefaultUserModelThreshold = UserModelThreshold{
	MinMessages: 10,
	MinSession:  5 * time.Minute,
}

// MeetsThreshold reports whether the candidate has accumulated enough
// interaction to warrant a model. OR semantics match the PRD.
func (t UserModelThreshold) MeetsThreshold(messageCount int, sessionDuration time.Duration) bool {
	return messageCount >= t.MinMessages || sessionDuration >= t.MinSession
}

// UserModelCandidate is one (user, workspace) pair the sweep is
// considering. CrewID records which crew's shared memory holds the
// file (used only for disk-path resolution); it is NOT part of the
// identity key.
type UserModelCandidate struct {
	WorkspaceID     string
	CrewID          string
	CrewSlug        string
	UserID          string
	MessageCount    int
	SessionDuration time.Duration
}

// UserModelOutcome is the per-candidate result a routine handler hands
// back to the scheduler for journal emission + metrics.
type UserModelOutcome struct {
	UserID   string
	UserSlug string
	Action   string // "write" | "skip_threshold" | "skip_opt_out" | "delete_opt_out" | "skip_empty_content"
	Bytes    int
	Reason   string
	Err      error
}

// SyncUserModel runs the per-candidate decision loop:
//
//  1. Opt-out check  → if true and a model exists, delete + audit; return.
//  2. Threshold check → if not met, skip with reason.
//  3. content == ""   → caller decided there's nothing to write yet;
//     skip with reason (distinguished from opt-out for metrics).
//  4. Otherwise        → WriteUserModel + upsert user_models row +
//     emit peer_card_audit 'write'.
//
// The function does NOT call the LLM and does NOT merge — the routine
// handler shapes the (already-merged) content via the aux model slot
// and passes the result in via `content` so this primitive stays unit-
// testable without a live model.
func SyncUserModel(
	ctx context.Context,
	db *sql.DB,
	logger *slog.Logger,
	threshold UserModelThreshold,
	cand UserModelCandidate,
	content string,
	paths memory.UserModelPaths,
	now time.Time,
) UserModelOutcome {
	slug := memory.UserSlug(cand.UserID, cand.WorkspaceID)
	out := UserModelOutcome{UserID: cand.UserID, UserSlug: slug}

	// 1) consent (shared with peer cards)
	optedOut, err := IsOptedOut(ctx, db, cand.UserID, cand.WorkspaceID)
	if err != nil {
		out.Action = "skip_opt_out"
		out.Err = err
		return out
	}
	if optedOut {
		// Opt-out is a hard stop: purge the model + index row. Propagate
		// errors so the routine summary counts a failed purge as a
		// failure rather than reporting GDPR cleanup complete.
		if err := memory.DeleteUserModel(paths, cand.UserID, cand.WorkspaceID); err != nil {
			out.Action = "delete_opt_out"
			out.Err = fmt.Errorf("delete user model on opt-out: %w", err)
			return out
		}
		if _, err := db.ExecContext(ctx, `
			DELETE FROM user_models
			WHERE workspace_id = ? AND user_slug = ?
		`, cand.WorkspaceID, slug); err != nil {
			out.Action = "delete_opt_out"
			out.Err = fmt.Errorf("delete user_models index on opt-out: %w", err)
			return out
		}
		recordAudit(ctx, db, logger, peerAuditRow{
			workspaceID:  cand.WorkspaceID,
			actorKind:    "system",
			action:       "delete",
			targetUserID: cand.UserID,
			metadata:     `{"reason":"opt_out","kind":"user_model"}`,
		})
		out.Action = "delete_opt_out"
		out.Reason = "user opted out"
		return out
	}

	// 2) threshold
	if !threshold.MeetsThreshold(cand.MessageCount, cand.SessionDuration) {
		out.Action = "skip_threshold"
		out.Reason = fmt.Sprintf("messages=%d session=%s below %d/%s",
			cand.MessageCount, cand.SessionDuration,
			threshold.MinMessages, threshold.MinSession)
		return out
	}

	// 3) extractor produced nothing yet
	if strings.TrimSpace(content) == "" {
		out.Action = "skip_empty_content"
		out.Reason = "extractor returned empty body"
		return out
	}

	// 4) write
	if err := memory.WriteUserModel(paths, cand.UserID, cand.WorkspaceID, content); err != nil {
		out.Action = "write"
		out.Err = fmt.Errorf("WriteUserModel: %w", err)
		return out
	}
	// Upsert the index row keyed on (workspace_id, user_slug) — NO
	// agent_id. ON CONFLICT DO UPDATE preserves the original id (stable
	// audit join) while bumping bytes + updated_at + crew_id on refresh.
	modelID := stableUserModelID(cand.WorkspaceID, slug)
	var crewID any
	if cand.CrewID != "" {
		crewID = cand.CrewID
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO user_models (id, workspace_id, crew_id, user_id, user_slug, path, bytes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, user_slug) DO UPDATE SET
		    crew_id = excluded.crew_id,
		    bytes = excluded.bytes,
		    updated_at = excluded.updated_at
	`, modelID, cand.WorkspaceID, crewID, cand.UserID, slug,
		paths.ModelPath(slug), len(content),
		now.UTC().Format(time.RFC3339Nano),
		now.UTC().Format(time.RFC3339Nano)); err != nil {
		out.Action = "write"
		out.Err = fmt.Errorf("user_models upsert: %w", err)
		return out
	}
	recordAudit(ctx, db, logger, peerAuditRow{
		workspaceID:  cand.WorkspaceID,
		actorKind:    "system",
		action:       "write",
		targetUserID: cand.UserID,
		metadata:     fmt.Sprintf(`{"bytes":%d,"kind":"user_model"}`, len(content)),
	})
	out.Action = "write"
	out.Bytes = len(content)
	return out
}

// stableUserModelID produces a deterministic identifier from
// (workspace_id, user_slug) so two routine runs landing concurrently
// can't insert duplicate rows. Short, prefixed, never carries PII.
func stableUserModelID(workspaceID, userSlug string) string {
	return "um_" + memory.UserSlug(workspaceID, userSlug)
}

// MergeUserModel folds a freshly-extracted model into the prior one,
// preserving prior fields the new extraction is silent about.
//
// This is the heart of the "evolving" model: a session that only
// touches one aspect of how the operator works must not erase the
// stable picture built over prior sessions. The merge is line-oriented
// on "- key: value" bullets:
//
//   - Lines present in BOTH (same key) take the new value (the latest
//     session is the more current signal for that field).
//   - Lines present only in the PRIOR are preserved verbatim (the new
//     transcript was silent → keep what we knew).
//   - Lines present only in the EXTRACTION are appended (new signal).
//
// Non-bullet lines (free-form prose) from the extraction are appended
// after the merged bullets so the LLM can still add a short narrative;
// prior free-form prose is dropped in favour of the latest narrative,
// since prose can't be merged field-wise. Keep extractor output bullet-
// shaped to get the field-preserving behaviour.
//
// Empty extraction → prior survives intact. Empty prior → extraction
// verbatim.
func MergeUserModel(prior, extracted string) string {
	prior = strings.TrimRight(prior, "\n")
	extracted = strings.TrimSpace(extracted)
	if extracted == "" {
		return prior
	}
	if strings.TrimSpace(prior) == "" {
		return extracted
	}

	priorFields, priorOrder, _ := splitFields(prior)
	newFields, newOrder, newProse := splitFields(extracted)

	// Start from prior order, overlaying any new values for shared keys.
	var b strings.Builder
	seen := map[string]bool{}
	for _, k := range priorOrder {
		val := priorFields[k]
		if nv, ok := newFields[k]; ok {
			val = nv // new value wins for a field the session re-touched
		}
		b.WriteString("- " + k + ": " + val + "\n")
		seen[k] = true
	}
	// Append brand-new fields the prior never had, in extraction order.
	for _, k := range newOrder {
		if seen[k] {
			continue
		}
		b.WriteString("- " + k + ": " + newFields[k] + "\n")
		seen[k] = true
	}
	// Append the latest free-form narrative (if any).
	for _, line := range newProse {
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// splitFields parses a model body into "- key: value" bullets (keyed,
// order-preserving) plus the leftover non-bullet prose lines. Keys are
// lower-cased + trimmed so "Timezone" and "timezone" merge as one
// field.
func splitFields(s string) (fields map[string]string, order []string, prose []string) {
	fields = map[string]string{}
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		bullet := strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if strings.HasPrefix(line, "-") && strings.Contains(bullet, ":") {
			idx := strings.Index(bullet, ":")
			key := strings.ToLower(strings.TrimSpace(bullet[:idx]))
			val := strings.TrimSpace(bullet[idx+1:])
			if key == "" {
				prose = append(prose, raw)
				continue
			}
			if _, ok := fields[key]; !ok {
				order = append(order, key)
			}
			fields[key] = val
			continue
		}
		prose = append(prose, raw)
	}
	// order is insertion-ordered, which is the stable contract the
	// merge relies on — prior fields keep their original position.
	return fields, order, prose
}

// BuildUserModelExtractionPrompt assembles the aux-LLM prompt that
// produces a refreshed user model. It embeds the PRIOR model so the
// model can merge rather than overwrite, the recent transcript, and
// the non-negotiable framing rules: bullet-shaped "- key: value"
// output, "hint not fact", and — critically — preserve prior fields
// the transcript is silent about.
//
// The deterministic MergeUserModel above is the belt-and-braces
// guarantee: even if the LLM ignores the preserve instruction, the
// caller folds the LLM output back over the prior model so silent
// fields survive. The prompt instruction is the suspenders.
func BuildUserModelExtractionPrompt(prior, transcript string) string {
	prior = strings.TrimSpace(prior)
	if prior == "" {
		prior = "(no prior model — this is the first extraction for this operator)"
	}
	return strings.Join([]string{
		"You maintain a small, evolving profile of how a single operator",
		"likes to work in this workspace. It is a HINT about communication",
		"style and working preferences, NOT a fact about the operator's",
		"identity or intent. Keep it under 1500 bytes.",
		"",
		"Output ONLY bullets in the form \"- key: value\" (e.g.",
		"\"- timezone: UTC+1\", \"- tone: terse, technical\"). One field",
		"per line. You may add at most one short free-form sentence after",
		"the bullets.",
		"",
		"MERGE RULES (important):",
		"- Start from the PRIOR model below.",
		"- Update a field only when the new transcript gives clear, fresh",
		"  signal for it.",
		"- PRESERVE every prior field the new transcript is silent about —",
		"  do not drop a field just because this session didn't mention it.",
		"- Add a new field only when the transcript clearly supports it.",
		"- When unsure, keep the prior value rather than guessing.",
		"",
		"PRIOR MODEL:",
		prior,
		"",
		"RECENT TRANSCRIPT:",
		transcript,
		"",
		"Refreshed model:",
	}, "\n")
}
