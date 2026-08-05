-- Give a routine an icon and a colour of its own.
--
-- A workspace ends up with dozens of routines and they are, in a list,
-- thirty identical rows of text. An icon is the cheapest thing that
-- makes one findable at a glance — the same job it already does for
-- crews and projects, using the same picker.
--
-- Why columns and not the definition. `display_name` and `description`
-- live inside definition_json, so the obvious move is to put icon there
-- too. It is the wrong home: definition_json is hashed into
-- definition_hash, the hash is what pipeline_versions is keyed on and
-- what the HMAC save_token binds to. Writing an icon into it would mint
-- a NEW ROUTINE VERSION every time somebody recoloured a row, and would
-- invalidate any save_token already issued for the old hash.
--
-- Appearance is not part of what the routine does, so it must not be
-- part of what the routine is versioned by. Columns keep the two apart:
-- recolouring touches these and nothing else, and the definition hash
-- is untouched.
--
-- Both are nullable with no default. NULL means "not set", and the UI
-- derives a stable icon from the slug in that case, so existing rows
-- keep the appearance they already had rather than all snapping to one
-- shared default on upgrade.

ALTER TABLE pipelines ADD COLUMN icon TEXT;
ALTER TABLE pipelines ADD COLUMN color TEXT;
