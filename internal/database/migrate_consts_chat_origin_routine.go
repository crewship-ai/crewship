package database

// migrationChatOriginRoutine backfills `chats.origin = 'ROUTINE'` onto the
// per-step chats the pipeline runner wrote BEFORE it learned to stamp one.
//
// Why a backfill at all: internal/api/chat_kinds.go partitions the
// conversations column on mode+origin, and a NULL origin classifies as
// `direct` — deliberately, so a row with an origin nobody has thought of yet
// stays visible rather than vanishing. That default is right for every future
// row and wrong for exactly this population: every routine step ever run on
// an instance that upgrades into this change is sitting in the table with a
// NULL origin, and would land in the one bucket the change exists to keep
// them out of. An instance would upgrade, read "your conversations are
// separated now", and see the same mixed list.
//
// The title match is a heuristic, and it is confined to here on purpose. It
// is safe in a migration in a way it is not safe in a query:
//
//   - It runs ONCE, against rows a known writer produced in a known format —
//     `fmt.Sprintf("Pipeline %s · step %s", ...)`, with a U+00B7 separator,
//     from internal/pipeline/runner_orchestrator.go. Nothing writes that
//     shape any more; the same code now stamps the origin directly.
//   - A title is user-editable (`PATCH .../chats/{id}`), so the same rule
//     evaluated on every render would reclassify a row the moment somebody
//     tidied its name — and would misfile a HUMAN conversation somebody
//     happened to call "Pipeline notes · step two".
//
// `created_by IS NULL` is the second half of the guard and the load-bearing
// one: a chat a person opened always carries their user id (both
// `POST /agents/{id}/chats` and the UI path set it), and the synthetic step
// chats never do — the internal endpoint's own comment says created_by is
// "NULL for system-initiated chats (routine dispatch)". A row has to be BOTH
// system-created and titled in the runner's exact format to be touched.
//
// Idempotent by construction (`origin IS NULL` is consumed by the write), and
// reversible by hand if it ever misfires: the rows it touches are identifiable
// after the fact by the same predicate plus `origin = 'ROUTINE'`.
const migrationChatOriginRoutine = `
UPDATE chats
   SET origin = 'ROUTINE'
 WHERE origin IS NULL
   AND created_by IS NULL
   AND mode <> 'MISSION'
   AND title LIKE 'Pipeline %' || char(183) || ' step %';
`
