-- Constrain workspace_members.role to the five roles that exist.
--
-- crew_members.role already carries
-- `CHECK(role IS NULL OR role IN ('OWNER','ADMIN','MANAGER','MEMBER','VIEWER'))`.
-- workspace_members.role — the column that decides what a member can do across
-- the entire workspace — carries nothing. It is `TEXT NOT NULL DEFAULT
-- 'MEMBER'` and will accept any string at all.
--
-- Today nothing writes a bad one: workspaces_membership.go validates against a
-- whitelist and refuses a non-OWNER assigning ADMIN, and every other insert
-- path writes a literal. So this is defense in depth, not a live hole. It is
-- worth having anyway because of an asymmetry in how the roles are read:
--
--     canRole(role, "create" | "update" | "manage" | "delete")  → switch over
--         the known roles, so an unrecognised value falls through to `false`.
--     canRole(role, "read")                                     → `continue`
--         for ANY non-empty string.
--
-- The write tiers fail closed on a garbage role. The read tier does not — it
-- only checks that the string is non-empty. So the one value that would matter
-- is exactly the one the application layer would let through, and the schema
-- is the only place that can refuse it independently of whichever write path
-- appears next.
--
-- WHY TRIGGERS RATHER THAN A CHECK CONSTRAINT
--
-- SQLite cannot add a CHECK to an existing table. Getting one requires the
-- 12-step table rebuild — create a new table, copy, drop, rename, recreate
-- every index and trigger — which in this codebase means the machinery in
-- migrate_consts_v167_journal_append_only_fks.go: a pinned connection because
-- `PRAGMA foreign_keys` is silently ignored inside a transaction, a
-- `legacy_alter_table` toggle, and poisoning the connection if the pragma
-- cannot be restored so the pool cannot hand a half-enforced connection to the
-- next borrower. That is a great deal of risk to take on the table that
-- decides who owns a workspace, for a constraint the application already
-- upholds.
--
-- A BEFORE INSERT / BEFORE UPDATE trigger pair enforces exactly the same
-- predicate, in plain SQL, with no rebuild. It is the pattern this schema
-- already uses for cross-row invariants — see
-- trg_credential_bindings_workspace_check (v-20260728135240) and the
-- append-only guards in v166.
--
-- The one behavioural difference is deliberate and, here, an advantage: a
-- trigger constrains WRITES and says nothing about rows already stored. A
-- rebuild with a CHECK would refuse to apply at all if some legacy row held an
-- unexpected role — turning a defensive tightening into a boot failure on the
-- exact instance that most needs looking at. A pre-existing odd row instead
-- survives, keeps working, and is refused the moment anything tries to write
-- it back.

CREATE TRIGGER IF NOT EXISTS trg_workspace_members_role_check
BEFORE INSERT ON workspace_members
BEGIN
    SELECT RAISE(ABORT, 'workspace_members.role must be one of OWNER, ADMIN, MANAGER, MEMBER, VIEWER')
    WHERE NEW.role NOT IN ('OWNER', 'ADMIN', 'MANAGER', 'MEMBER', 'VIEWER');
END;

CREATE TRIGGER IF NOT EXISTS trg_workspace_members_role_check_upd
BEFORE UPDATE ON workspace_members
BEGIN
    SELECT RAISE(ABORT, 'workspace_members.role must be one of OWNER, ADMIN, MANAGER, MEMBER, VIEWER')
    WHERE NEW.role NOT IN ('OWNER', 'ADMIN', 'MANAGER', 'MEMBER', 'VIEWER');
END;
