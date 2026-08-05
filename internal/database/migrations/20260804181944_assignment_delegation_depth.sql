-- Where an assignment sits in the delegation tree (#1754).
--
-- Delegation had no bound: a sub-agent that dispatches again was limited by
-- nothing, and the only related control — /query's `depth >= 2` — reads its
-- depth out of the request body the calling agent writes, so it is bypassed by
-- sending {"depth":0}. A cap is only a cap if the number it reads is one the
-- agent cannot write, which means it has to live here, in a row the agent has
-- no path to.
--
-- depth is the number of delegation hops from the originating run: a lead's
-- own dispatch is 1, what THAT sub-agent dispatches is 2. parent_assignment_id
-- is the assignment the dispatcher was executing when it dispatched, so the
-- chain stays reconstructible after the fact (and so fan-out can be counted
-- against one parent rather than against a whole chat).
--
-- Existing rows keep depth 0 / parent NULL: they were created before the
-- concept existed and are treated as roots, which is what they were. 0 is
-- deliberately NOT a valid depth for a new row — the first dispatch is 1 — so
-- a legacy row can never be mistaken for one this code wrote.

ALTER TABLE assignments ADD COLUMN depth INTEGER NOT NULL DEFAULT 0;

ALTER TABLE assignments ADD COLUMN parent_assignment_id TEXT
    REFERENCES assignments(id) ON DELETE SET NULL;

-- The fan-out count for a delegated run is "children of this parent", run on
-- every /assign from a sub-agent.
CREATE INDEX IF NOT EXISTS idx_assignment_parent
    ON assignments (parent_assignment_id);

-- The parent lookup is "the in-flight assignment this agent is executing":
-- assigned_to_id is already indexed (idx_assignment_to), status is the other
-- half of that predicate.
CREATE INDEX IF NOT EXISTS idx_assignment_to_status
    ON assignments (assigned_to_id, status);
