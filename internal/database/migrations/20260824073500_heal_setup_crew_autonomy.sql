-- One-time heal for the Crewship Guide's autonomy level, moved here out of
-- ensureOnboardingSetupCrew (internal/api/onboarding_setup_crew.go).
--
-- setupCrewAutonomyLevel changed from 'strict' to 'full'. INSERT OR IGNORE
-- only ever applies a constant to a brand-new row, so a workspace whose setup
-- crew was created under the old default would carry 'strict' forever. The
-- fix for that was an UPDATE ... WHERE autonomy_level = 'strict' on the
-- request path, guarded that way so it would not overwrite an operator who
-- had moved the crew somewhere else.
--
-- That guard cannot do what it claims. 'strict' set by the old default and
-- 'strict' set by `crewship policy set --crew _crewship-setup --level strict`
-- are the same four bytes in the same column — crew_policy.go writes exactly
-- the column this reads. So the request-path heal silently re-escalated the
-- one full-autonomy crew in the workspace every time an operator lowered it,
-- and it was not a one-shot: GET /api/v1/onboarding/status calls
-- ensureOnboardingSetupCrew on every poll for any workspace holding a
-- credential, onboarding finished or not, because the Guide stays a
-- conversation partner afterwards. An operator hardening their instance would
-- have had the change reverted within seconds, with nothing logged.
--
-- A migration is the honest home for a one-time backfill: it runs once per
-- database, so "only heals the old default, never an operator's choice"
-- becomes true by construction rather than by a WHERE clause that cannot
-- tell the two apart. Anything an operator sets after this point stays set.
--
-- Scoped to the reserved system crew by slug AND kind: '_crewship-setup' is
-- unavailable to user-created crews (validSlugFormat / makeSlug cannot emit a
-- leading underscore), and kind='setup' is server-written, so this cannot
-- reach a crew a person made.
UPDATE crews
SET autonomy_level = 'full',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE slug = '_crewship-setup'
  AND kind = 'setup'
  AND autonomy_level = 'strict';
