-- Crews are soft-deleted; their links were not deleted at all. Every reseed
-- therefore left a full set of links behind pointing at crews nobody can see
-- (24 of 27 rows on a dev instance), and the settings page listed them all.
--
-- The handler now deletes a crew's links with the crew and the list filters
-- soft-deleted ends, so this only has to clear what earlier builds left.
-- Deleting is safe: a link whose end is deleted can never be enforced — every
-- consumer resolves the crew first — so nothing observable changes.
DELETE FROM crew_connections
WHERE from_crew_id IN (SELECT id FROM crews WHERE deleted_at IS NOT NULL)
   OR to_crew_id   IN (SELECT id FROM crews WHERE deleted_at IS NOT NULL);
