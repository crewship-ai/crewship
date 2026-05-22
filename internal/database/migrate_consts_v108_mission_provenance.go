package database

// migrationMissionProvenance (v108) adds the same provenance trio
// that `pipelines` carries (author_chat_id, author_run_id,
// authored_via) to the `missions` table. Before this migration,
// missions only tracked the LEAD agent that owns them — two missions
// solved by two different agent runs in two different chats were
// indistinguishable at the row level. Routines / pipelines have had
// full authorship since v78; aligning missions closes that gap and
// powers the F4.5 mission-outcomes-to-crew-memory hook
// (.claude/context/prd/MISSION-OUTCOMES-TO-MEMORY.md).
//
// All three columns are nullable so legacy rows remain valid without
// a backfill — the lesson hook only reads these fields when a NEW
// terminal-state transition fires, so a NULL on an old row is fine.
//
// authored_via mirrors the closed enum on pipelines.authored_via,
// extended with two values implicit in current code paths:
//
//   - 'routine'   — mission row created as the side-effect of a routine
//     invocation (recorded today via missions.routine_id
//     but the authorship channel was not stamped)
//   - 'recurring' — mission row created by the recurring_issues
//     scheduler (cron-driven, see migration v42-v45)
//
// Net-new column on an existing table with default NULL — SQLite
// recreate dance is unnecessary. Indices land alongside the columns
// so /api/v1/missions?author_chat_id=X queries stay fast on the
// 10k+-row workspaces that have routines firing continuously.
const migrationMissionProvenance = `
ALTER TABLE missions ADD COLUMN author_chat_id TEXT;
ALTER TABLE missions ADD COLUMN author_run_id TEXT;
ALTER TABLE missions ADD COLUMN authored_via TEXT
    CHECK (authored_via IS NULL OR authored_via IN
        ('agent_tool_call','user_api','imported','seed','routine','recurring'));

CREATE INDEX IF NOT EXISTS idx_mission_chat ON missions(author_chat_id)
    WHERE author_chat_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_mission_run ON missions(author_run_id)
    WHERE author_run_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_mission_authored_via ON missions(authored_via)
    WHERE authored_via IS NOT NULL;
`
