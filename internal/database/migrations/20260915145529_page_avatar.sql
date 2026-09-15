-- A page gets its own icon and colour (#2563) — an avatar, the way a crew, an
-- agent and a page folder have one — so the rail can tell eight pages apart
-- by something other than their freshness glyph.
--
-- Two columns rather than two keys in spec_json, and deliberately so. The
-- spec is the page's CONTRACT (panels, owners, producers, SLAs) and it is what
-- versions, rollbacks and export bundles carry. The avatar is presentation,
-- like folder_id: it is not restored by a rollback, not carried by a bundle,
-- and not part of the fingerprint that fires `refresh: on:panels-changed`.
--
-- Both vocabularies are the crew icon registry and the crew palette, refused
-- by name at save time (internal/api/pages_folder_icons.go). NULL means "none":
-- the client draws its default page glyph, in no colour, and "no colour" and
-- "blue" never look the same.
ALTER TABLE pages ADD COLUMN icon TEXT;
ALTER TABLE pages ADD COLUMN color TEXT;
