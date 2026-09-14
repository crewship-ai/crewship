-- The memory mutation contract: a CAS anchor and a durable mutation ledger.
--
-- Contract: docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §8, with
-- §3's "Memory mutation" entity row (scope + key + operation ID; request hash,
-- base/target revision and hash, durable intent, the content or durable
-- reference recovery needs, provenance and result) and invariant I6.
--
-- This does NOT replace memory_versions. That table is the audit trail — one
-- row per write event, content-addressed, forward-only. It has no revision, and
-- its parent_sha is written and displayed but never compared, so it cannot
-- refuse a stale write. These two tables are what refuse it.

-- The CAS anchor: one row per key, carrying the monotonic revision.
--
-- `path` is the SCOPED audit path — 'agent:<slug>/AGENT.md',
-- 'crew:<id>/CREW.md' — the same vocabulary internal/memory/audit_watcher.go
-- already writes into memory_versions.path. Keying on the bare filename would
-- collide every agent's AGENT.md in a workspace onto one anchor, which is a
-- lost-update generator rather than a CAS.
CREATE TABLE memory_revisions (
    workspace_id TEXT NOT NULL,
    path TEXT NOT NULL,

    -- Denormalised from the path for querying: 'agent:<slug>' / 'crew:<id>',
    -- and the memory_versions tier vocabulary.
    scope TEXT NOT NULL DEFAULT '',
    tier TEXT NOT NULL DEFAULT '',

    -- Monotonic per (workspace, path). A write that leaves the bytes unchanged
    -- keeps the revision: the revision is a content identity clients compare,
    -- and two numbers for identical bytes would make cached expectations
    -- spuriously stale.
    revision INTEGER NOT NULL,
    -- SHA-256 of the exact bytes the confirmed mutation left on disk. The drift
    -- check compares this against the real file before every write; a mismatch
    -- is memory_conflict, never a silent merge.
    content_sha256 TEXT NOT NULL,
    bytes INTEGER NOT NULL DEFAULT 0,

    last_mutation_id TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,

    PRIMARY KEY (workspace_id, path)
);

CREATE INDEX idx_memory_revisions_scope ON memory_revisions(workspace_id, scope, path);

-- One proposed change, from the durable intent through to the confirmation.
--
-- The row is written BEFORE the file is touched and updated after, so a crash
-- anywhere in between leaves evidence. §8's recovery protocol reads the file's
-- hash and compares it against base_sha256 and target_sha256 to decide what
-- happened; target_blob_ref is the durable data that lets recovery finish a
-- write whose author is gone.
CREATE TABLE memory_mutations (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    path TEXT NOT NULL,                        -- the scoped audit key
    canonical_path TEXT NOT NULL,              -- absolute on-disk path, so recovery can find the file
    scope TEXT NOT NULL DEFAULT '',
    tier TEXT NOT NULL DEFAULT '',

    -- Stable across retries of the same logical write. An identical retry
    -- returns the original result; the same id with a different request is
    -- operation_conflict, which is what request_sha256 decides.
    operation_id TEXT NOT NULL,
    request_sha256 TEXT NOT NULL,
    op TEXT NOT NULL,                          -- append | replace

    -- §3 provenance. run_id and generation are internal/work's fencing pair
    -- (I4); they are recorded here and verified by the caller's authorization
    -- hook, because internal/memory cannot see the work ledger.
    source TEXT NOT NULL DEFAULT '',
    actor_type TEXT NOT NULL DEFAULT '',       -- agent | user | system | consolidator
    actor_id TEXT NOT NULL DEFAULT '',
    run_id TEXT NOT NULL DEFAULT '',
    generation INTEGER NOT NULL DEFAULT 0,

    base_revision INTEGER NOT NULL DEFAULT 0,
    base_sha256 TEXT NOT NULL DEFAULT '',
    new_revision INTEGER NOT NULL,
    target_sha256 TEXT NOT NULL,
    target_bytes INTEGER NOT NULL DEFAULT 0,
    -- Content-addressed blob under {memoryRoot}/versions, the same store
    -- memory_versions already uses. §8: "Nepotvrzený intent musí obsahovat
    -- durable data pro dokončení."
    target_blob_ref TEXT NOT NULL DEFAULT '',

    -- The declared removals, exactly as verified: [{start_line, line_count,
    -- old_sha256}] in the base revision's normalised line numbering.
    removals_json TEXT NOT NULL DEFAULT '[]',
    -- 0 means the caller declared NOTHING (not "declared that nothing is
    -- removed") and the real diff was never checked against a declaration. §8
    -- read strictly refuses that; refusing it would break every memory.write
    -- the model has ever issued, so it is accepted and recorded here instead.
    -- An unverified replace must be auditable as unverified.
    removals_declared INTEGER NOT NULL DEFAULT 0,

    -- The §8 explicit-import flags. adopted_base: no anchor existed and this
    -- mutation took the pre-existing file as revision 0. normalized_base: the
    -- on-disk base was rewritten from CRLF. Both are recorded because §8
    -- forbids either happening silently.
    adopted_base INTEGER NOT NULL DEFAULT 0,
    normalized_base INTEGER NOT NULL DEFAULT 0,
    -- 1 when the drift check was deliberately skipped because the whole point
    -- of the operation was to overwrite whatever is on disk. Restore is the
    -- only such operation. §8's "never automatically overwrite" governs
    -- unattended RECOVERY; this column is what keeps the difference between
    -- "a human asked" and "a process decided" auditable.
    override_drift INTEGER NOT NULL DEFAULT 0,

    -- intent -> renamed -> confirmed is §8's state machine. `conflicted` is the
    -- fourth outcome recovery can reach: the file matched neither hash, so a
    -- third party owns those bytes and they are not overwritten. It settles the
    -- row so the pending-intent index releases, while the anchor stays stale so
    -- every later write of the key still conflicts until someone imports it.
    state TEXT NOT NULL,
    result_json TEXT NOT NULL DEFAULT '{}',

    -- Bitemporal metadata, §8's last paragraph. recorded_at is when the system
    -- learned it; valid_from/valid_to are when the claim itself holds and are
    -- NULL unless a caller actually knows, because §8 forbids inventing
    -- validity. The two refs point at the mutation this one corrects or
    -- retracts.
    recorded_at TEXT NOT NULL,
    confirmed_at TEXT,
    valid_from TEXT,
    valid_to TEXT,
    corrects_mutation_id TEXT REFERENCES memory_mutations(id),
    retracts_mutation_id TEXT REFERENCES memory_mutations(id),

    UNIQUE(workspace_id, path, operation_id),
    CHECK (op IN ('append','replace')),
    CHECK (state IN ('intent','renamed','confirmed','conflicted'))
);

-- One pending intent per key. This is what blocks a second writer while
-- recovery is still possible — a database fact, not a convention, and the only
-- part of the contract that survives the process that took the flock dying.
CREATE UNIQUE INDEX idx_memory_mutations_pending
    ON memory_mutations(workspace_id, path)
    WHERE state IN ('intent','renamed');

CREATE INDEX idx_memory_mutations_key ON memory_mutations(workspace_id, path, recorded_at);
CREATE INDEX idx_memory_mutations_unsettled ON memory_mutations(state) WHERE state IN ('intent','renamed');
CREATE INDEX idx_memory_mutations_run ON memory_mutations(run_id, recorded_at) WHERE run_id != '';
-- Both correction refs lead an index. They are self-referential foreign keys,
-- and memory retention DOES delete mutation history, so without these each such
-- delete full-scans memory_mutations to enforce the constraint while holding
-- SQLite's single write lock — the policy in
-- internal/database/migrate_index_hot_foreign_keys_test.go.
CREATE INDEX idx_memory_mutations_corrects ON memory_mutations(corrects_mutation_id) WHERE corrects_mutation_id IS NOT NULL;
CREATE INDEX idx_memory_mutations_retracts ON memory_mutations(retracts_mutation_id) WHERE retracts_mutation_id IS NOT NULL;
