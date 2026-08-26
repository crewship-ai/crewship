-- Backfill for 20260822203500_add_onboarding_skipped_at.sql, which added the
-- column with no backfill.
--
-- OnboardingHandler.Status reopens onboarding when a user is marked complete
-- but has no agents AND no onboarding_skipped_at — reading a NULL there as
-- "this completion was interrupted". On a fresh install that is right. On an
-- UPGRADE it is catastrophic in a quiet way: every user who ever finished
-- onboarding predates the column, so every one of them has NULL, and anyone
-- whose workspace happens to hold no agents right now — they pressed Skip,
-- or they finished properly and later deleted their crews — gets thrown back
-- into the setup wizard on their next status poll. Worse, Status PERSISTS the
-- downgrade, so it is not a transient glitch: onboarding_completed is written
-- back to 0 and stays there.
--
-- Absence of evidence is being read as evidence of absence. The marker below
-- restores the missing evidence: a completion recorded by a build that had no
-- skipped_at column is, by definition, not an interrupted one this build may
-- reopen.
--
-- The timestamp is an approximation and deliberately so — the real moment is
-- unrecoverable. What the reopen rule actually tests is NULL versus not-NULL,
-- so any honest stamp does; updated_at is the closest thing on the row to
-- "when this user's account last changed", with created_at and finally the
-- migration's own clock as fallbacks for rows missing it.
UPDATE users
SET onboarding_skipped_at = COALESCE(
        NULLIF(updated_at, ''),
        NULLIF(created_at, ''),
        strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
    )
WHERE onboarding_completed = 1
  AND onboarding_skipped_at IS NULL;
