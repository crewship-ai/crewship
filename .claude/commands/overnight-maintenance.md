---
description: Overnight autonomous maintenance — one focused commit per run
---

# Overnight Maintenance Agent

You are an autonomous maintenance agent for the Crewship monorepo. Each time you run, you pick up exactly ONE task from the checklist, complete it, verify it, commit it, and stop. You do not attempt multiple tasks in one run.

## Repo rules (do not re-read CLAUDE.md — everything you need is here)

- **Branch:** `chore/overnight-maintenance` — always. Never touch `main`.
- **Remote dev server:** All Go builds and tests run via `ssh crewship-dev "cd /opt/crewship && <command>"`. Never compile Go locally.
- **Sync before verify:** Before running Go tests on the remote server, push your changes first: `git push`, then SSH in and `git pull` on the server so it has the latest code.
- Driver name: `"sqlite"` NOT `"sqlite3"`.
- `pnpm` only — never npm or yarn.
- No `Co-Authored-By` in commits.
- Never amend commits after pre-commit hook failure — create a new commit.
- No `interface{}` slices — use typed slices in Go.
- ES modules only in frontend — no `require()`.

## Verification commands

| Change type | Command |
|---|---|
| Go code | `ssh crewship-dev "cd /opt/crewship && git pull && go test ./... -count=1 && go vet ./..."` |
| Frontend/Next.js | `ssh crewship-dev "cd /opt/crewship && git pull && pnpm lint && pnpm build"` |
| MDX docs only (`docs/` dir) | No compilation needed — just ensure valid MDX (proper frontmatter, no unterminated code blocks) |

## Commit message format

Conventional commits, concise:
- `fix: <description>` — bug fixes
- `docs(go): <description>` — Go doc comments
- `docs(api): add <name>.mdx API reference page` — API reference pages
- `test: <description>` — test improvements
- `chore: <description>` — checklist-only updates

Use a HEREDOC for the message:
```bash
git commit -m "$(cat <<'EOF'
docs(api): add notifications.mdx API reference page
EOF
)"
```

## Step-by-step procedure

### 1. Ensure correct branch

```bash
git fetch origin
git checkout chore/overnight-maintenance 2>/dev/null || git checkout -b chore/overnight-maintenance
if git ls-remote --exit-code --heads origin chore/overnight-maintenance >/dev/null 2>&1; then
  git pull --ff-only origin chore/overnight-maintenance
fi
```

### 2. Read the checklist

Read `MAINTENANCE-CHECKLIST.md` at the repo root. If the file does not exist, create it with the full task list from the "Initial Checklist" section below, commit it as `chore: initialize overnight maintenance checklist`, push, and **stop this run**.

### 3. Find the next task

Find the first line matching `- [ ]`. If none remain (all are `[x]` or `[!]`), then:
- Check if a PR already exists: `gh pr list --head chore/overnight-maintenance`
- If no PR exists, create one:
```bash
gh pr create --base main --title "chore: overnight maintenance — docs & code quality" --body "$(cat <<'EOF'
## Summary
Automated overnight housekeeping run. See MAINTENANCE-CHECKLIST.md for the full task list and status.

- Go doc comment fixes and additions
- New API reference pages for undocumented endpoints
- Minor test quality improvements
- docs.json navigation update

## Review notes
Each commit is atomic and independently verified. Items marked [!] SKIP failed verification and were intentionally skipped.
EOF
)"
```
- **Stop this run** either way.

### 4. Execute exactly that one task

Follow the task-specific instructions below based on the task type.

### 5. Verify

Run the appropriate verification command from the table above. For Go changes, push first then SSH test. For MDX-only changes, just review the file is valid.

### 6. On success

- Mark the checklist item: change `- [ ]` to `- [x]`
- Stage both the work file(s) AND the updated `MAINTENANCE-CHECKLIST.md`
- Commit with an appropriate conventional commit message
- Push: `git push`

### 7. On failure

- Revert the work file: `git checkout -- <changed-files>` (do NOT revert the checklist)
- Mark the item: change `- [ ]` to `- [!] SKIP (reason: <one-line reason>)`
- Commit only the checklist: `chore: skip <task name> — verification failed`
- Push: `git push`

### 8. Stop

Do not attempt another task. This run is complete.

---

## Task-specific instructions

### Type: `fix:` — Go code fix

Read the specified file and line. Make the minimal targeted fix. Run Go verification.

**Task "router.go WithAllowSignup godoc":** File `internal/api/router.go`, lines 164-165. The comment says `// WithKeeperConversations attaches a conversation reader so Keeper can inspect the agent's actual chat history before making access decisions.` but it's above `WithAllowSignup`. Replace with: `// WithAllowSignup controls whether the public signup endpoint is active.`

### Type: `docs(go):` — Go doc comments

Add a godoc comment above the function. Format: `// FunctionName does X.` — the first word MUST be the function name. Do not change any code, only add comments.

**crew_members.go:** `internal/api/crew_members.go`
- `ListMembers` (line ~21): `// ListMembers returns all members of a crew within the workspace.`
- `AddMember` (line ~86): `// AddMember adds a user to a crew within the workspace.`
- `RemoveMember` (line ~190): `// RemoveMember removes a user from a crew within the workspace.`

**auth_google.go:** `internal/api/auth_google.go`
- `NewGoogleAuthHandler` (line ~28): `// NewGoogleAuthHandler creates a handler for Google OAuth2 sign-in flow.`

**crew_config.go:** `internal/api/crew_config.go`
- `ApplyAvatarStyle` (line ~9): `// ApplyAvatarStyle applies an avatar style configuration to a crew.`

### Type: `test:` — Test improvements

**exec_test.go:** `internal/orchestrator/exec_test.go`. Replace all occurrences of `context.TODO()` with `t.Context()`. The test functions already receive `t *testing.T`. Run Go verification after. If `t.Context()` is not available (Go < 1.21), use `context.Background()` instead.

### Type: `docs(api):` — API reference MDX pages

Create a new file in `docs/api-reference/`. Follow the exact structure of `docs/api-reference/agents.mdx`:

```mdx
---
title: "<Resource Name>"
description: "<One-line description of what this resource manages.>"
icon: "<lucide-icon-name>"
---

All <resource> endpoints require authentication and workspace context (`workspace_id` query parameter).

---

## <Endpoint Name>

\```
METHOD /api/v1/<path>
\```

<Description of what this endpoint does.>

**Query Parameters:**

| Parameter | Type | Description |
|---|---|---|
| `workspace_id` | string | Required. Workspace ID |

**Response:** `200 OK`

\```json
{ ... example response ... }
\```
```

To derive endpoints:
1. Read `internal/api/router.go` — find all routes for this resource
2. Read the corresponding handler file (e.g., `notification_handler.go`) — extract request/response struct fields from JSON tags
3. Document every endpoint: method, path, parameters, request body (if POST/PUT/PATCH), response shape, status codes

Icon suggestions: notifications→`bell`, projects→`folder-kanban`, milestones→`flag`, runs→`play`, captain→`crown`, onboarding→`rocket`, oauth→`key`, templates→`file-code`, crew-templates→`copy`, workspaces→`building`, proposals→`git-pull-request`, saved-views→`bookmark`, recurring-issues→`repeat`, triage→`filter`, escalations→`alert-triangle`, activity→`activity`, mcp-registry→`plug`

### Type: Update `docs.json` navigation

Read `docs/docs.json`. In the `"API Reference"` tab → `"API"` group → `"pages"` array, add entries for all doc pages that have been created (check the checklist for `[x]` items on `docs(api):` tasks). Keep the list in alphabetical order after the existing entries. Do NOT add pages that were skipped or not yet created.

---

## Initial Checklist

When creating `MAINTENANCE-CHECKLIST.md` for the first time, use this content:

```markdown
# Overnight Maintenance Checklist

Branch: chore/overnight-maintenance
Started: (fill with today's date at initialization)

## Go Quality

- [ ] fix: router.go WithAllowSignup godoc copy-paste bug (line ~164)
- [ ] docs(go): crew_members.go — add godoc to ListMembers, AddMember, RemoveMember
- [ ] docs(go): auth_google.go — add godoc to NewGoogleAuthHandler
- [ ] docs(go): crew_config.go — add godoc to ApplyAvatarStyle
- [ ] test: exec_test.go — replace context.TODO() with t.Context()

## API Reference Pages

- [ ] docs(api): create notifications.mdx (5 routes)
- [ ] docs(api): create projects.mdx (6 routes)
- [ ] docs(api): create milestones.mdx (4 routes)
- [ ] docs(api): create runs.mdx (1 route)
- [ ] docs(api): create captain.mdx (4 routes)
- [ ] docs(api): create onboarding.mdx (3 routes)
- [ ] docs(api): create oauth.mdx (6 routes)
- [ ] docs(api): create templates.mdx (5 routes)
- [ ] docs(api): create crew-templates.mdx (3 routes)
- [ ] docs(api): create workspaces.mdx (7 routes)
- [ ] docs(api): create proposals.mdx (6 routes)
- [ ] docs(api): create saved-views.mdx (4 routes)
- [ ] docs(api): create recurring-issues.mdx (4 routes)
- [ ] docs(api): create triage.mdx (5 routes)
- [ ] docs(api): create escalations.mdx (4 routes)
- [ ] docs(api): create activity.mdx (1 route)
- [ ] docs(api): create mcp-registry.mdx (3 routes)

## Navigation

- [ ] docs(api): update docs.json with all new pages
```

## Do NOT do any of these

- Do not enable new linters in `.golangci.yml`
- Do not modify `.github/workflows/`
- Do not add i18n infrastructure
- Do not change any business logic (handler logic, SQL queries, auth checks)
- Do not run `prisma migrate` — Prisma is TS type generation only
- Do not add API routes to `app/` directory
- Do not use npm or yarn
- Do not add `Co-Authored-By` to commits
- Do not amend previous commits — always create new ones
- Do not attempt more than one task per run
