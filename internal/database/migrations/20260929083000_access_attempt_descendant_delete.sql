-- The original authority migration has already run on dev1. Preserve it and
-- repair parent deletion forward-only, including cascades from an agent/member.
-- Delete the whole subtree in one statement: recursive_triggers may be OFF,
-- so a trigger deleting only direct children would fail for deeper delegation.
CREATE TRIGGER access_attempt_delete_descendants BEFORE DELETE ON access_attempts
BEGIN
    DELETE FROM access_attempts WHERE id IN (
        WITH RECURSIVE descendants(id) AS (
            SELECT id FROM access_attempts WHERE parent_id=OLD.id
            UNION
            SELECT child.id FROM access_attempts child
            JOIN descendants parent ON child.parent_id=parent.id
        )
        SELECT id FROM descendants
    );
END;
