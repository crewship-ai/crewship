-- The durable work ledger: one owner for claim, retry and recovery.
--
-- Contract: docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §3 (the
-- entities and invariants I1-I8) and §4 (the state machine). The tables named
-- there are an implementation contract, not a description of what existed.
--
-- These tables do NOT replace assignments, pending_runs or pipeline_runs. They
-- take ownership of *dispatch* — which piece of work runs next, on whose lease,
-- and what happens when that lease is lost. The domain tables keep their own
-- rows and link to a work item; §3 forbids a third competing queue.

-- One accepted unit of work. Stable across every attempt: a retry keeps the
-- work_id and mints a new run_id. Manual replay of terminal work creates a NEW
-- work_id carrying replay_of, per §4.
CREATE TABLE work_items (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,

    -- Where it came from, and the domain row it stands for. source_ref is the
    -- delivery / mailbox message / schedule fire that produced it.
    source TEXT NOT NULL,                     -- webhook | chat | assignment | schedule | pipeline_step | manual
    source_ref TEXT NOT NULL DEFAULT '',
    domain_kind TEXT NOT NULL DEFAULT '',     -- assignment | pipeline_run | agent_run | ''
    domain_id TEXT NOT NULL DEFAULT '',

    -- Who it runs as and inside which conversation. session_id is the chat
    -- session; §7 allows at most one active turn per session, and background
    -- work uses a separate session rather than borrowing the chat's.
    agent_id TEXT NOT NULL DEFAULT '',
    crew_id TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    class TEXT NOT NULL DEFAULT 'background', -- chat | background; §6 reserves capacity per class

    -- The authorization that admitted it. Re-checked at dispatch, per I8: a
    -- permission removed while the work waited must take effect on dispatch.
    authorized_by_user_id TEXT NOT NULL DEFAULT '',
    authorized_scope TEXT NOT NULL DEFAULT '',

    -- The immutable input and the version of the thing it was accepted
    -- against. A replay against a different target revision must be explicit.
    input_json TEXT NOT NULL DEFAULT '{}',
    input_sha256 TEXT NOT NULL DEFAULT '',
    target_revision TEXT NOT NULL DEFAULT '',

    state TEXT NOT NULL,
    state_reason TEXT NOT NULL DEFAULT '',
    -- Bumped on every claim. The fencing term: a transition carrying a stale
    -- generation is refused, so a worker that lost its lease cannot overwrite
    -- the attempt that replaced it (I5). The River spike measured a queue that
    -- lacks exactly this and accepted the stale write.
    generation INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0,

    priority INTEGER NOT NULL DEFAULT 0,
    eligible_at TEXT NOT NULL,
    deadline_at TEXT,                          -- NULL = never expires on its own
    replay_of TEXT REFERENCES work_items(id),
    replay_reason TEXT NOT NULL DEFAULT '',

    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    terminal_at TEXT,

    CHECK (state IN (
        'queued','starting','running','waiting','retry_wait',
        'succeeded','failed','expired','cancelled','needs_reconciliation'
    )),
    CHECK (class IN ('chat','background'))
);

-- The dispatch scan: eligible, non-terminal work, oldest first within a class.
CREATE INDEX idx_work_items_dispatch
    ON work_items(state, class, eligible_at, priority, created_at)
    WHERE state IN ('queued','retry_wait');
CREATE INDEX idx_work_items_workspace ON work_items(workspace_id, created_at, id);
-- One active turn per session. The predicate is wider than the capacity one:
-- `waiting` gives back its execution slot but the conversation is still
-- mid-turn.
CREATE INDEX idx_work_items_session ON work_items(session_id, state) WHERE session_id != '';
-- The per-agent and per-workspace capacity counts the claim scan runs for every
-- candidate row. needs_reconciliation is in the predicate on purpose: it holds
-- an execution slot, because the runtime under its locator may still be alive.
CREATE INDEX idx_work_items_agent_live
    ON work_items(agent_id, class)
    WHERE state IN ('starting','running','needs_reconciliation');
CREATE INDEX idx_work_items_workspace_live
    ON work_items(workspace_id)
    WHERE state IN ('starting','running','needs_reconciliation');
CREATE INDEX idx_work_items_deadline ON work_items(deadline_at) WHERE deadline_at IS NOT NULL;
-- replay_of points at another work item, and retention DOES hard-delete terminal
-- work. Without this index each such delete full-scans work_items to enforce the
-- constraint, while holding the single write lock — see the foreign-key index
-- policy in internal/database/migrate_index_hot_foreign_keys_test.go.
CREATE INDEX idx_work_items_replay_of ON work_items(replay_of) WHERE replay_of IS NOT NULL;

-- One attempt. run_id is the SAME namespace as agent_runs.id, so the journal's
-- existing run_id index keeps working instead of gaining a second meaning.
CREATE TABLE work_attempts (
    run_id TEXT PRIMARY KEY,
    work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    attempt INTEGER NOT NULL,
    generation INTEGER NOT NULL,

    lease_owner TEXT NOT NULL,
    lease_expires_at TEXT NOT NULL,
    heartbeat_at TEXT NOT NULL,

    -- Where the runtime actually is, so recovery can look before it decides a
    -- process is gone. §4: an expired lease alone does not authorize a second
    -- process.
    runtime_locator TEXT NOT NULL DEFAULT '',

    started_at TEXT NOT NULL,
    start_reason TEXT NOT NULL DEFAULT '',
    ended_at TEXT,
    end_reason TEXT NOT NULL DEFAULT '',
    exit_evidence TEXT NOT NULL DEFAULT '',
    cost_usd REAL NOT NULL DEFAULT 0,

    UNIQUE(work_id, attempt)
);

CREATE INDEX idx_work_attempts_live
    ON work_attempts(lease_expires_at)
    WHERE ended_at IS NULL;
CREATE INDEX idx_work_attempts_work ON work_attempts(work_id, attempt);

-- The append-only history. Authoritative state and its event are written in one
-- transaction, per §3; the sequence is monotonic per work item.
CREATE TABLE work_events (
    work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    at TEXT NOT NULL,
    from_state TEXT NOT NULL DEFAULT '',
    to_state TEXT NOT NULL,
    run_id TEXT NOT NULL DEFAULT '',
    generation INTEGER NOT NULL DEFAULT 0,
    reason TEXT NOT NULL DEFAULT '',
    detail_json TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY (work_id, seq)
);

-- The delivery ledger W7 found missing: today the only record is four columns
-- on the endpoint row. A delivery is never deleted on failure — a retry becomes
-- a new attempt of the same work, and a manual replay a new work item.
CREATE TABLE webhook_deliveries (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    endpoint_id TEXT NOT NULL,
    endpoint_kind TEXT NOT NULL,              -- agent | routine | page
    profile TEXT NOT NULL,                    -- github | standard-webhooks | legacy-agent | legacy-routine

    -- Identity is (workspace, endpoint, source delivery id). W5: it must not be
    -- derived from the signature, which changes with a fresh timestamp.
    source_delivery_id TEXT NOT NULL,
    -- The profile's content replay key. GitHub does not sign its delivery id,
    -- so §5 also suppresses byte-identical raw bodies on one endpoint.
    content_key TEXT NOT NULL DEFAULT '',

    body_sha256 TEXT NOT NULL,
    body_bytes INTEGER NOT NULL DEFAULT 0,
    -- Raw body is kept at least until terminal, then to its own retention.
    -- Absent means a replay is unavailable and the UI has to say so.
    raw_body BLOB,
    raw_body_expires_at TEXT,

    event_type TEXT NOT NULL DEFAULT '',
    event_action TEXT NOT NULL DEFAULT '',
    signing_key_id TEXT NOT NULL DEFAULT '',

    -- What the filter decided, and why. An ignored delivery is audited, not
    -- dropped: §5 requires the decision be recoverable.
    filter_decision TEXT NOT NULL,            -- accepted | ignored
    filter_reason TEXT NOT NULL DEFAULT '',
    target_revision TEXT NOT NULL DEFAULT '',

    work_id TEXT REFERENCES work_items(id),
    received_at TEXT NOT NULL,
    dedup_expires_at TEXT NOT NULL,

    UNIQUE(workspace_id, endpoint_id, source_delivery_id),
    CHECK (filter_decision IN ('accepted','ignored'))
);

CREATE UNIQUE INDEX idx_webhook_deliveries_content
    ON webhook_deliveries(workspace_id, endpoint_id, content_key)
    WHERE content_key != '';
CREATE INDEX idx_webhook_deliveries_list ON webhook_deliveries(workspace_id, received_at, id);
CREATE INDEX idx_webhook_deliveries_dedup_expiry ON webhook_deliveries(dedup_expires_at);
CREATE INDEX idx_webhook_deliveries_work ON webhook_deliveries(work_id) WHERE work_id IS NOT NULL;

-- Evidence for effects outside this database. The intent is written BEFORE the
-- call, the receipt after. A crash between them is `unknown`, and §4 requires
-- reconciliation rather than a blind retry. This does not make any external
-- effect exactly-once; it makes an ambiguous one visible.
CREATE TABLE external_operations (
    id TEXT PRIMARY KEY,                       -- stable across retries of the work
    work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL DEFAULT '',
    op_type TEXT NOT NULL,
    target TEXT NOT NULL DEFAULT '',
    request_sha256 TEXT NOT NULL,
    idempotency_key TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,                       -- intent | succeeded | failed | unknown
    provider_receipt TEXT NOT NULL DEFAULT '', -- never a secret; §3
    created_at TEXT NOT NULL,
    settled_at TEXT,
    CHECK (state IN ('intent','succeeded','failed','unknown'))
);

CREATE INDEX idx_external_operations_work ON external_operations(work_id, created_at);
CREATE INDEX idx_external_operations_unsettled ON external_operations(state) WHERE state IN ('intent','unknown');

-- The durable chat mailbox P1 found missing: today a busy agent bounces the
-- message before it is persisted and the user has to retype it. Order is the
-- server sequence assigned in the accepting transaction, not arrival order at
-- some later pump.
CREATE TABLE session_mailbox (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    client_message_id TEXT NOT NULL,
    seq INTEGER NOT NULL,
    body TEXT NOT NULL,
    attachments_json TEXT NOT NULL DEFAULT '[]',
    state TEXT NOT NULL,                       -- pending | claimed | delivered | cancelled
    work_id TEXT REFERENCES work_items(id),
    created_at TEXT NOT NULL,
    delivered_at TEXT,
    -- A resend of the same client message returns the same row rather than
    -- queueing a second turn, per §7.
    UNIQUE(workspace_id, user_id, session_id, client_message_id),
    UNIQUE(session_id, seq),
    CHECK (state IN ('pending','claimed','delivered','cancelled'))
);

CREATE INDEX idx_session_mailbox_next
    ON session_mailbox(session_id, seq)
    WHERE state = 'pending';
-- Same reason as work_items.replay_of: the parent is pruned by retention.
CREATE INDEX idx_session_mailbox_work ON session_mailbox(work_id) WHERE work_id IS NOT NULL;
