package pipeline

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
)

// InboxNotifier is the sink a notify step writes to. Splitting it out from
// *sql.DB keeps runNotifyStep unit-testable (a fake captures the composed
// inbox.Item) and mirrors the store seam wait:approval uses for the same
// inbox table.
type InboxNotifier interface {
	Notify(ctx context.Context, item inbox.Item) error
}

// sqlInboxNotifier is the production InboxNotifier: it forwards to
// inbox.Insert, whose (kind, source_id) unique index makes retries
// idempotent. Constructed from the run's own DB in NewWiredExecutor.
type sqlInboxNotifier struct {
	db     *sql.DB
	logger *slog.Logger
}

func (n *sqlInboxNotifier) Notify(ctx context.Context, item inbox.Item) error {
	return inbox.Insert(ctx, n.db, n.logger, item)
}

// WithInboxNotifier wires the notify-step inbox sink. Without it, notify
// steps degrade to a best-effort no-op (logged) rather than failing the run.
func (e *Executor) WithInboxNotifier(n InboxNotifier) *Executor {
	e.notifier = n
	return e
}

// perRunNotifyCap is the per-recipient soft cap on routine-update notices a
// single run may deliver to one inbox. A DAG that puts a notify in every
// step (progress spam) or targets one user from many branches shouldn't be
// able to dump an unbounded pile of cards on that recipient. It's a SOFT
// cap: once reached, further notices to that recipient are dropped with a
// WARN — never a run failure. The bound is per (run, recipient), so a run
// notifying several people each gets its own budget, and a scheduled routine
// firing repeatedly is unaffected (each run has a fresh, distinct run id).
const perRunNotifyCap = 20

// WithNoticeCounter wires the per-recipient anti-spam counter for notify
// steps. Without it, the soft cap is skipped and delivery is uncapped
// (dev/test/misconfig). Production installs NewRunNoticeCounter(db).
func (e *Executor) WithNoticeCounter(fn func(ctx context.Context, workspaceID, runID, targetUserID, targetRole string) (int, error)) *Executor {
	e.noticeCounter = fn
	return e
}

// WithMemberChecker wires the workspace-membership guard for notify steps
// that target `user:<id>`. Without it, the guard is skipped and the id is
// trusted as-is.
func (e *Executor) WithMemberChecker(fn func(ctx context.Context, workspaceID, userID string) (bool, error)) *Executor {
	e.memberCheck = fn
	return e
}

// WithCrewAudienceResolver wires the crew→human-audience lookup for notify
// steps that target `crew:<slug>`. It mirrors WithMemberChecker exactly (a
// plain func seam, DB-free package): production installs
// NewCrewAudienceResolver(db); without it a crew: target degrades to a
// workspace notice rather than failing the run.
func (e *Executor) WithCrewAudienceResolver(fn func(ctx context.Context, workspaceID, crewSlug string) ([]string, error)) *Executor {
	e.crewAudience = fn
	return e
}

// runNotifyStep handles StepNotify: a non-blocking push of a rendered
// message to a recipient's inbox mid-run. It renders title/body from the
// run context, scrubs secrets, resolves the target, and emits a `message`
// inbox item keyed on run:step (idempotent on retry), then returns so the
// DAG continues. Delivery is best-effort and NEVER fails the run — the
// routine's real work is already done. A literal malformed `to`/`priority`
// is caught at author time (offline Validate / draft dry-run save gate);
// at run time we degrade rather than crash, so a templated target that
// renders empty (e.g. `user:{{ inputs.assignee }}` → `user:`) or a typo'd
// member id can never take the run down with it.
func (e *Executor) runNotifyStep(ctx context.Context, step Step, parentRender RenderContext, in RunInput, runID string) (out string, cost float64, dur int64, err error) {
	stepStart := time.Now()
	if step.Notify == nil {
		return "", 0, 0, fmt.Errorf("notify step %q missing notify body", step.ID)
	}
	// {{ secrets.<type> }} in a notification renders the vault value, but a
	// notice is broadcast to workspace members — so the resolved value MUST
	// be scrubbed back out of the title/body (below) and any output/error
	// (deferred). This is defense: an author who references a secret in a
	// notice can never actually leak it.
	var secrets *secretScrub
	parentRender, secrets = e.resolveStepSecrets(ctx, step, parentRender, in)
	defer func() { out, err = secrets.scrub(out), secrets.scrubErr(err) }()

	// Resolve the target. A run-time resolution failure (templated `to`
	// rendered empty, malformed input value) must NOT fail the run — the
	// non-blocking contract. Author mistakes with LITERAL targets are
	// already rejected by validateNotifyTarget at save/dry-run; here we
	// degrade to a workspace-wide notice so the update still lands.
	// degraded tracks whether the author's intended recipient could NOT be
	// honoured and the notice fell back to a workspace-wide notice. It changes
	// the return marker (notified:degraded) and surfaces a run warning, so a
	// silent fallback isn't indistinguishable from a targeted delivery — in
	// particular a templated `to` that renders to an unsupported `crew:<slug>`
	// (which can only be caught here at run time, not at save) is visible on
	// the run rather than a server-log-only footnote.
	degraded := false
	toRaw := strings.TrimSpace(Render(step.Notify.To, parentRender))
	recipients, crewSlug, terr := resolveNotifyTargets(toRaw, in.InvokingUserID)
	if terr != nil {
		slog.Default().Warn("notify step: unresolvable target — falling back to workspace notice",
			"run", runID, "step", step.ID, "to", toRaw, "error", terr)
		e.recordRunWarning(ctx, runID, "notify:"+step.ID,
			fmt.Errorf("target %q not delivered as addressed — sent as a workspace notice instead: %w", toRaw, terr))
		recipients, crewSlug = workspaceNotice(), ""
		degraded = true
	}

	// dry_run preview: render but never write to the inbox (and never touch
	// the DB to expand a crew audience).
	if in.Mode == ModeDryRun {
		return "notified:preview", 0, time.Since(stepStart).Milliseconds(), nil
	}

	// crew:<slug> fan-out. The audience lookup is a workspace-scoped seam
	// (NewCrewAudienceResolver); an unknown slug, an empty crew, a lookup
	// error or an unwired resolver all degrade to a workspace notice —
	// never a run failure.
	crewFanout := false
	if crewSlug != "" {
		var members []string
		var aerr error
		if e.crewAudience != nil {
			members, aerr = e.crewAudience(ctx, in.WorkspaceID, crewSlug)
		}
		switch {
		case e.crewAudience == nil:
			// No resolver wired (dev/test/misconfig). Degrade quietly to a
			// workspace notice — the update still lands.
			slog.Default().Warn("notify step: no crew audience resolver wired — falling back to workspace notice",
				"run", runID, "step", step.ID, "crew", crewSlug)
			recipients = workspaceNotice()
			degraded = true
		case aerr != nil:
			slog.Default().Warn("notify step: crew audience lookup failed — falling back to workspace notice",
				"run", runID, "step", step.ID, "crew", crewSlug, "error", aerr)
			e.recordRunWarning(ctx, runID, "notify:"+step.ID,
				fmt.Errorf("crew %q audience could not be resolved — sent as a workspace notice instead: %w", crewSlug, aerr))
			recipients = workspaceNotice()
			degraded = true
		case len(members) == 0:
			// Unknown slug and empty crew are indistinguishable here on
			// purpose: both mean "nobody to address", and both must land
			// the update somewhere rather than vanish.
			slog.Default().Warn("notify step: crew has no members (or no such crew) — falling back to workspace notice",
				"run", runID, "step", step.ID, "crew", crewSlug)
			e.recordRunWarning(ctx, runID, "notify:"+step.ID,
				fmt.Errorf("crew %q has no members in this workspace — sent as a workspace notice instead", crewSlug))
			recipients = workspaceNotice()
			degraded = true
		default:
			recipients = make([]notifyRecipient, 0, len(members))
			for _, uid := range members {
				recipients = append(recipients, notifyRecipient{UserID: uid})
			}
			crewFanout = true
		}
	}

	// Membership guard: a user target that isn't in the workspace would be
	// a silent black hole — the row inserts, but nobody can ever see it.
	// Non-members are dropped from the recipient set + WARNed; if that
	// empties the set, the notice degrades to a workspace notice so a typo'd
	// id doesn't swallow the message. (A DB error checking membership fails
	// open to the intended target rather than dropping it.)
	if e.memberCheck != nil {
		kept := make([]notifyRecipient, 0, len(recipients))
		for _, r := range recipients {
			if r.UserID == "" {
				kept = append(kept, r)
				continue
			}
			member, merr := e.memberCheck(ctx, in.WorkspaceID, r.UserID)
			switch {
			case merr != nil:
				slog.Default().Warn("notify step: membership check failed — targeting user anyway",
					"run", runID, "step", step.ID, "user", r.UserID, "error", merr)
				kept = append(kept, r)
			case !member:
				slog.Default().Warn("notify step: target user not in workspace — dropping recipient",
					"run", runID, "step", step.ID, "user", r.UserID)
				e.recordRunWarning(ctx, runID, "notify:"+step.ID,
					fmt.Errorf("target user %q is not a workspace member — not delivered to them", r.UserID))
			default:
				kept = append(kept, r)
			}
		}
		if len(kept) != len(recipients) {
			degraded = true
		}
		if len(kept) == 0 {
			recipients = workspaceNotice()
			crewFanout = false
		} else {
			recipients = kept
		}
	}

	senderName := "routine"
	if in.dsl != nil && in.dsl.Name != "" {
		senderName = in.dsl.Name
	}
	// Layer the exact {{ secrets.* }} value scrub over the shape-based
	// RedactSecrets so an opaque vault value (which RedactSecrets' known-
	// token regexes would miss) can never land in a broadcast inbox row.
	title := inbox.CleanTitle(secrets.scrub(inbox.RedactSecrets(Render(step.Notify.Title, parentRender))), 120, senderName)
	body := secrets.scrub(inbox.RedactSecrets(Render(step.Notify.Body, parentRender)))
	// baseSourceID keeps the historical one-item-per-(run, step) idempotency
	// key. A crew fan-out writes one item per member, so each carries a
	// per-recipient suffix — still fully deterministic, so re-running the
	// same run id can't double-post to anyone.
	baseSourceID := runID + ":" + step.ID

	if e.notifier == nil {
		// Production always wires a notifier (NewWiredExecutor with a DB);
		// nil is dev/test/misconfig. Non-blocking philosophy: don't fail
		// the run because there's nowhere to post.
		slog.Default().Warn("notify step skipped: no inbox notifier wired", "run", runID, "step", step.ID)
		return "notified:skipped", 0, time.Since(stepStart).Milliseconds(), nil
	}

	delivered, capped, failed := 0, 0, 0
	for _, r := range recipients {
		// Per-recipient anti-spam soft cap: if this run has already delivered
		// perRunNotifyCap notices to this recipient, drop this one. The cap is
		// enforced at the notify chokepoint (not the inbox writer) so it counts
		// only routine notices for THIS run, keyed on the same target the item
		// will carry. Best-effort: a counter error fails OPEN (deliver) —
		// anti-spam is a courtesy, the update landing is the priority.
		if e.noticeCounter != nil {
			switch prior, cerr := e.noticeCounter(ctx, in.WorkspaceID, runID, r.UserID, r.Role); {
			case cerr != nil:
				slog.Default().Warn("notify step: notice count failed — delivering uncapped",
					"run", runID, "step", step.ID, "error", cerr)
			case prior >= perRunNotifyCap:
				slog.Default().Warn("notify step: per-recipient soft cap reached — dropping notice",
					"run", runID, "step", step.ID, "user", r.UserID, "role", r.Role, "cap", perRunNotifyCap)
				capped++
				continue
			}
		}

		sourceID := baseSourceID
		if crewFanout {
			sourceID = baseSourceID + ":" + r.UserID
		}
		item := inbox.Item{
			WorkspaceID:  in.WorkspaceID,
			Kind:         inbox.KindMessage,
			SourceID:     sourceID, // idempotency: one item per (run, step, recipient)
			TargetUserID: r.UserID,
			TargetRole:   r.Role,
			Title:        title,
			BodyMD:       body,
			SenderType:   "pipeline",
			SenderName:   senderName,
			Priority:     normalizeNotifyPriority(step.Notify.Priority),
			Blocking:     false, // update, not a decision request
			Payload: map[string]interface{}{
				// subkind keeps routine updates in their own filterable lane
				// so they don't drown approvals/escalations in the inbox.
				"subkind":         "routine_update",
				"pipeline_run_id": runID,
				"step_id":         step.ID,
			},
		}
		if crewFanout {
			item.Payload["crew_slug"] = crewSlug
		}

		// Bound the delivery so a stalled inbox write can't hold the run
		// open: this is a fire-and-forget update, not part of the run's
		// critical path. A timeout collapses into the same non-fatal WARN
		// as any other delivery failure.
		if err := e.deliverNotice(ctx, item); err != nil {
			// A delivery failure is non-fatal: the routine did its work, the
			// bell just didn't ring. Log at WARN and carry on.
			slog.Default().Warn("notify step delivery failed",
				"run", runID, "step", step.ID, "user", r.UserID, "error", err)
			failed++
			continue
		}
		delivered++
	}

	switch {
	case delivered == 0 && capped > 0:
		return "notified:capped", 0, time.Since(stepStart).Milliseconds(), nil
	case delivered == 0 && failed > 0:
		return "notified:error", 0, time.Since(stepStart).Milliseconds(), nil
	}

	// Push it live. inbox.Insert only writes the row; the "inbox.updated"
	// WS fan-out lives in the API handler layer, so without this a routine
	// notify would only surface on the next poll. Broadcasting here makes
	// open inboxes + bell badges repaint immediately (use-inbox.ts
	// invalidates on any inbox.updated). Best-effort: e.ws is nil in tests.
	if e.ws != nil {
		e.ws.BroadcastWorkspace(in.WorkspaceID, "inbox.updated", map[string]string{
			"source":  "routine_notify",
			"run_id":  runID,
			"step_id": step.ID,
		})
	}
	// A degraded delivery landed, but NOT (only) to the recipient the author
	// addressed — mark it distinctly so the step output and run detail don't
	// read as a clean targeted send.
	if degraded {
		return "notified:degraded:" + baseSourceID, 0, time.Since(stepStart).Milliseconds(), nil
	}
	return "notified:" + baseSourceID, 0, time.Since(stepStart).Milliseconds(), nil
}

// deliverNotice writes one composed item to the inbox under the notify
// step's fire-and-forget delivery budget.
func (e *Executor) deliverNotice(ctx context.Context, item inbox.Item) error {
	notifyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return e.notifier.Notify(notifyCtx, item)
}

// NewWorkspaceMemberChecker returns a membership predicate backed by the
// workspace_members table — used by notify steps to avoid posting to a
// user id that isn't in the workspace (a silent black hole). Nil db or
// empty ids report "not a member" without a query.
func NewWorkspaceMemberChecker(db *sql.DB) func(ctx context.Context, workspaceID, userID string) (bool, error) {
	return func(ctx context.Context, workspaceID, userID string) (bool, error) {
		if db == nil || workspaceID == "" || userID == "" {
			return false, nil
		}
		var exists int
		err := db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM workspace_members WHERE workspace_id = ? AND user_id = ?)`,
			workspaceID, userID).Scan(&exists)
		if err != nil {
			return false, err
		}
		return exists == 1, nil
	}
}

// NewRunNoticeCounter returns the production per-recipient notice counter
// backing perRunNotifyCap. It counts routine-update `message` items already
// written for this run (SourceID = "<run>:<step>", so the "<run>:" prefix
// selects exactly this run's notices) to the same recipient the pending
// item will target. Recipient match mirrors resolveNotifyTarget's output:
//   - user:  target_user_id = <id>
//   - role:  target_role = <ROLE> with no user (a role notice)
//   - workspace: neither set
//
// Nil db reports 0 (cap disabled) so callers never fail the run over it.
func NewRunNoticeCounter(db *sql.DB) func(ctx context.Context, workspaceID, runID, targetUserID, targetRole string) (int, error) {
	return func(ctx context.Context, workspaceID, runID, targetUserID, targetRole string) (int, error) {
		if db == nil || workspaceID == "" || runID == "" {
			return 0, nil
		}
		// The stored id is "ibx_message_<run>:<step>"; match on source_id,
		// which the writer sets to "<run>:<step>", via the "<run>:" prefix.
		// LIKE metacharacters (%, _) can't appear in a cuid run id, so no
		// escaping is needed.
		prefix := runID + ":%"
		var n int
		err := db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM inbox_items
			WHERE workspace_id = ?
			  AND kind = ?
			  AND source_id LIKE ?
			  AND target_user_id IS ?
			  AND target_role IS ?`,
			workspaceID, inbox.KindMessage, prefix,
			nullableStr(targetUserID), nullableStr(targetRole),
		).Scan(&n)
		if err != nil {
			return 0, err
		}
		return n, nil
	}
}

// notifyRecipient is one addressed inbox audience: a specific user, a role
// (no user), or — with both empty — the whole workspace.
type notifyRecipient struct {
	UserID string
	Role   string
}

// workspaceNotice is the single-recipient set meaning "workspace-wide" —
// the universal degrade target when an addressed recipient can't be honoured.
func workspaceNotice() []notifyRecipient { return []notifyRecipient{{}} }

// resolveNotifyTargets maps a notify `to` selector to the SET of inbox
// recipients it addresses. Most selectors resolve to exactly one recipient;
// `crew:<slug>` can't be resolved purely (it needs a workspace-scoped DB
// lookup), so it comes back as a non-empty crewSlug with a nil set and the
// caller expands it through the crew-audience seam.
func resolveNotifyTargets(to, invokingUserID string) (recipients []notifyRecipient, crewSlug string, err error) {
	switch {
	case to == "", to == "workspace":
		return workspaceNotice(), "", nil
	case to == "trigger":
		// Server-side the executor holds the triggering user. When it's
		// absent (scheduled run, nested call_pipeline), fall back to a
		// workspace notice so the update still lands.
		return []notifyRecipient{{UserID: invokingUserID}}, "", nil
	case strings.HasPrefix(to, "user:"):
		id := strings.TrimSpace(strings.TrimPrefix(to, "user:"))
		if id == "" {
			return nil, "", fmt.Errorf("to: user: missing id")
		}
		return []notifyRecipient{{UserID: id}}, "", nil
	case strings.HasPrefix(to, "role:"):
		r := strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(to, "role:")))
		switch r {
		case "OWNER", "MANAGER":
			return []notifyRecipient{{Role: r}}, "", nil
		default:
			return nil, "", fmt.Errorf("to: role:%s not targetable (allowed: OWNER, MANAGER)", r)
		}
	case strings.HasPrefix(to, "crew:"):
		// crew:<slug> addresses the crew's HUMAN audience — every row in
		// crew_members for that crew — so the notice fans out to the people
		// who own the crew's agents. The expansion is workspace-scoped in
		// NewCrewAudienceResolver; a slug can never reach another tenant.
		slug := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(to, "crew:")))
		if slug == "" {
			return nil, "", fmt.Errorf("to: crew: missing slug")
		}
		return nil, slug, nil
	default:
		return nil, "", fmt.Errorf("to %q unsupported (allowed: workspace, trigger, user:<id>, role:OWNER, role:MANAGER, crew:<slug>)", to)
	}
}

// validateNotifyTarget is the author-time shape check for `to`. Templated
// targets (containing {{ }}) are resolved at run time, so their literal
// shape is only checked once rendered.
func validateNotifyTarget(to string) error {
	if strings.Contains(to, "{{") {
		return nil
	}
	_, _, err := resolveNotifyTargets(to, "placeholder")
	return err
}

// NewCrewAudienceResolver returns the production crew→human-audience lookup
// backing `to: crew:<slug>`. It is WORKSPACE-SCOPED by construction: the
// crew is selected by (workspace_id, slug) — the pair the schema makes
// unique — so a slug from one tenant can never address another tenant's
// crew, even if both use the same slug. Soft-deleted crews resolve to no
// audience. Nil db or empty ids report an empty audience without a query,
// which the notify step degrades to a workspace notice.
func NewCrewAudienceResolver(db *sql.DB) func(ctx context.Context, workspaceID, crewSlug string) ([]string, error) {
	return func(ctx context.Context, workspaceID, crewSlug string) ([]string, error) {
		if db == nil || workspaceID == "" || crewSlug == "" {
			return nil, nil
		}
		rows, err := db.QueryContext(ctx, `
			SELECT DISTINCT cm.user_id
			FROM crew_members cm
			JOIN crews c ON c.id = cm.crew_id
			WHERE c.workspace_id = ?
			  AND c.slug = ?
			  AND c.deleted_at IS NULL
			ORDER BY cm.user_id`,
			workspaceID, crewSlug)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		var users []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			if id != "" {
				users = append(users, id)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return users, nil
	}
}

// isValidNotifyPriority reports whether p is one of the inbox priorities.
func isValidNotifyPriority(p string) bool {
	switch p {
	case "urgent", "high", "medium", "low":
		return true
	default:
		return false
	}
}

// normalizeNotifyPriority passes through a valid priority; anything else
// (including empty) defers to the inbox writer's medium default.
func normalizeNotifyPriority(p string) string {
	if isValidNotifyPriority(p) {
		return p
	}
	return ""
}
