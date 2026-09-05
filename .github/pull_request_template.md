## Description

<!-- Briefly describe what this PR does -->

## Type of Change

- [ ] Bug fix (non-breaking change fixing an issue)
- [ ] New feature (non-breaking change adding functionality)
- [ ] Breaking change (fix or feature that would cause existing functionality to not work as expected)
- [ ] Documentation update
- [ ] Refactoring (no functional changes)
- [ ] Performance improvement

## Changes Made

- 
- 

## Testing

- [ ] Unit tests added/updated
- [ ] Manual testing performed
- [ ] Build succeeds (`pnpm build`)
- [ ] Lint passes (`pnpm lint`)
- [ ] Go tests pass (`go test ./...`) *(if Go changes)*

## Changelog (enforced by the `Changelog Guard` workflow)

A PR touching `internal/api/`, `internal/orchestrator/`, `internal/harbormaster/`,
`cmd/crewship/`, `app/`, `components/`, `lib/`, `hooks/` or `stores/` needs an
entry under `## [Unreleased]` in
`CHANGELOG.md` — `RELEASING.md` cuts release notes from that section, so
an omission ships silently.

- [ ] Entry added under `## [Unreleased]`, marked `⚠️ **Behaviour change:**`
      if something that used to work can now fail. The guard compares that
      section specifically — an edit elsewhere in the file does not count
- [ ] …or the `skip-changelog` label is applied because this change has no
      user-visible effect (chore / internal refactor / test-only)

## Security Checklist

- [ ] No plaintext credentials or API keys in code
- [ ] RBAC checks on all new API endpoints
- [ ] Container changes maintain non-root (UID 1001) + --internal network

## Deployment Notes

- [ ] Database migrations included
- [ ] Environment variables updated (check `.env.example`)
- [ ] No deployment changes needed

## Migration Safety (skip if no migrations)

Migrations are forward-only and append-only — once a migration ships in `main`,
its version and name are immutable. The `migration-lint` workflow enforces this,
but please confirm:

- [ ] New migration's `version` is strictly greater than every existing one
- [ ] No existing migration in `main` was renamed, renumbered, or had its `sql` changed
- [ ] Schema change is backwards-compatible (no `DROP COLUMN` of a column still read by code)
- [ ] If adding a non-nullable column, a `DEFAULT` is provided OR a `restoreBackfill` hook is wired

## Checklist

- [ ] Self-review completed
- [ ] Commits follow conventional commits (`feat:`, `fix:`, `refactor:`)
- [ ] No console.log in production code
