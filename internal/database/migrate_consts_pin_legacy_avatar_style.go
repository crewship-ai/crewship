package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// migrationPinLegacyAvatarStyle pins every agent that draws with the default
// avatar style to the default it has today, bottts-neutral, before the default
// moves to Voxel Bot (#3789).
//
// Why a migration: an agent with no avatar_style of its own, in a crew with no
// avatar_style (or in no crew), is drawn with DEFAULT_AVATAR_STYLE from
// lib/agent-avatar.ts. Moving that constant would change the face of every
// such agent on every surface — most surfaces draw from (seed, style) rather
// than the stored render, and the stored-render rollout is lazy, so many
// agents have none. Writing the old default into exactly those rows keeps
// each face as it is; the new default then reaches only agents created after
// the upgrade.
//
// What it does NOT touch, on purpose:
//   - agents with a style of their own, and agents that follow a crew which
//     has a style — neither resolves to the default;
//   - crews: a crew with no style is how a NEW agent in it gets Voxel Bot;
//   - stored renders (avatar_svg): they depict exactly the pinned style.
//
// Soft-deleted agents are included: history views still draw them. Idempotent
// (the predicate is consumed by the write), which the restore path relies on.
const migrationPinLegacyAvatarStyle = `
UPDATE agents
   SET avatar_style = 'bottts-neutral'
 WHERE (avatar_style IS NULL OR avatar_style = '')
   AND (crew_id IS NULL
        OR crew_id NOT IN (SELECT id FROM crews WHERE avatar_style IS NOT NULL AND avatar_style <> ''));
`

// restoreBackfillPinLegacyAvatarStyle applies the same pin to rows restored
// from a backup taken before this migration, which come back with the old
// meaning of "no style".
func restoreBackfillPinLegacyAvatarStyle(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	res, err := tx.ExecContext(ctx, migrationPinLegacyAvatarStyle)
	if err != nil {
		return fmt.Errorf("pin legacy avatar style on restored agents: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 && logger != nil {
		logger.Info("pinned restored agents to the pre-#3789 avatar style", "agents", n)
	}
	return nil
}
