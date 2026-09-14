-- Close the window between starting a runtime and recording where it is.
--
-- Review finding R4. Claim created an attempt with no locator, StartRunning
-- wrote one only once the runtime was confirmed live, and recovery read "no
-- locator" as proof that no process had been created. A crash between the
-- actual start and that write broke the proof: recovery would requeue work
-- whose process was still running, and a second process would join the first.
--
-- The fix is to say what we are about to do before we do it. An attempt now
-- carries a phase, and the locator is written BEFORE the external start rather
-- than after it — deterministic, so recovery can look for the runtime by an
-- identity it knew in advance instead of inferring absence from silence.
--
--   planned    claimed, capacity reserved, nothing outside the database has
--              happened. Safe to requeue: there is no process to collide with.
--   starting   the locator is durable and an external start has been REQUESTED.
--              Nothing may assume the process does or does not exist; recovery
--              must look.
--   confirmed  the runtime answered. Same obligation as starting, plus we know
--              it was alive at least once.
--
-- Only `planned` licenses a blind requeue, and that is the whole point of the
-- column: the previous code had no way to distinguish "never started" from
-- "started and we died before writing it down".
ALTER TABLE work_attempts ADD COLUMN runtime_phase TEXT NOT NULL DEFAULT 'planned';

-- A durable cancel request against a specific attempt.
--
-- Review finding R1. Cancel read the work item's state outside any transaction
-- and then asked for `cancelled` with no precondition, so a claim landing in
-- between turned a live run's row terminal while its process kept going — and
-- the API said "cancelled before it started; no runtime was involved".
--
-- A cancel of live work is now a REQUEST, recorded here against the generation
-- that was current when it was made, and it survives a restart. `cancelled` is
-- reached only when a worker confirms the stop. Queued work is still cancelled
-- outright, but atomically, under the state read inside the same transaction.
ALTER TABLE work_attempts ADD COLUMN cancel_requested_at TEXT;
ALTER TABLE work_attempts ADD COLUMN cancel_requested_by TEXT NOT NULL DEFAULT '';
ALTER TABLE work_attempts ADD COLUMN cancel_reason TEXT NOT NULL DEFAULT '';

-- The dispatcher's scan for attempts it must chase: a cancel nobody has
-- confirmed yet, or a runtime whose phase means recovery has to look rather
-- than assume.
CREATE INDEX idx_work_attempts_cancel_pending
    ON work_attempts(cancel_requested_at)
    WHERE cancel_requested_at IS NOT NULL AND ended_at IS NULL;
