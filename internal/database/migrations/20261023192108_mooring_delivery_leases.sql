-- Runtime admission metadata only. Content and durable receipts stay elsewhere.
CREATE TABLE mooring_delivery_gates (
 ordinal INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE RESTRICT,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
 application_id TEXT NOT NULL,
 participant_ref TEXT NOT NULL,
 owner_id TEXT NOT NULL,
 owner_generation INTEGER NOT NULL CHECK(owner_generation>0),
 owner_token_hash BLOB NOT NULL CHECK(length(owner_token_hash)=32),
 permission_version TEXT NOT NULL,
 boot_id TEXT NOT NULL,
 expires_ns INTEGER NOT NULL CHECK(expires_ns>=0),
 renewal_nonce TEXT NOT NULL,
 closed INTEGER NOT NULL DEFAULT 0 CHECK(closed IN(0,1)),
 blocked INTEGER NOT NULL DEFAULT 0 CHECK(blocked IN(0,1))
);
CREATE INDEX mooring_delivery_gates_scope ON mooring_delivery_gates(page_id, participant_ref);
CREATE TABLE mooring_delivery_barriers (
 id TEXT PRIMARY KEY,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE RESTRICT,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
 application_id TEXT NOT NULL,
 participant_ref TEXT NOT NULL,
 cutoff_ordinal INTEGER NOT NULL CHECK(cutoff_ordinal>0),
 UNIQUE(page_id,workspace_id,application_id,participant_ref,cutoff_ordinal)
);

CREATE INDEX mooring_delivery_gates_workspace ON mooring_delivery_gates(workspace_id);
CREATE INDEX mooring_delivery_barriers_workspace ON mooring_delivery_barriers(workspace_id);
