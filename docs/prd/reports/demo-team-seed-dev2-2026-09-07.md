# Demo colleagues — Dev2, 2026-09-07

Implemented the [demo team PRD](../demo-team-chat.md) and deployed the working
tree to Dev2 through `systemctl reload crewship-ws@2`. Changes remain uncommitted
on `fix/chat-workspace-foundations`; no sibling instance was touched.

## Available demo

Six fictional colleagues now have separate normal accounts and bundled portraits:
Thomas ADMIN (platform), Paul MANAGER (product), Peter MEMBER (engineering),
Anna MANAGER (design), Sofia MEMBER (quality), Emma VIEWER (stakeholder).

[Open Team lounge · demo](https://crewship-dev2.unifylab.cz/chat?conversation=cmtrb4eokd7a22899a6d2a267fec03874&workspace_id=cmtplws8h000276fa560f).
It contains an owner introduction and six planning messages, each sent through
the named person's own authenticated session. Six private conversations with
the owner contain separate greetings. The existing owner and earlier Klára/Tomáš
fixtures were preserved. No agent work or real issue change was triggered.

The existing demo entry point `./dev.sh seed` includes this pack by default.
Only the additive command was run against the existing Dev2 workspace:

```sh
/tmp/crewship-2-dev --profile dev2 --server http://localhost:8082 \
  seed team-chat --state-dir /srv/crewship/.team-chat-demo-dev2
```

Run the same command to reuse the accounts and conversations. Credentials live
in protected state outside Git; stdout contains only identities, roles, room IDs
and the state path. Normal reruns preserve changed profiles and avatars and
refuse unexpected role changes. They do not reset passwords.

## Verified on the live instance

- The additive seed completed twice; results have identical six user IDs and
  seven conversation IDs. State is 0600 inside a 0700 scoped directory.
- Normal password and CSRF login succeeded for all six accounts.
- All six actual workspace roles match the catalog. Every served avatar is a
  distinct PNG whose SHA-256 matches its bundled asset.
- The channel has exactly six separately authored demo planning messages and
  one owner introduction. Each account can retry its own seeded message with
  the same ID without adding a duplicate.
- Harmless validation/nonexistent-resource probes checked provisioning, issue
  editing and member role gates. VIEWER can chat under the current product
  policy but cannot edit issues or manage members.
- Every account was denied another colleague's private DM. All six non-creator
  accounts were denied changes to this channel's activity settings.
- Emma's desktop and mobile Chat load all six portraits, roster and composer
  with zero browser errors.

[Machine evidence](demo-team-seed-dev2-2026-09-07.json),
[desktop screenshot](assets/chat-seed-team-dev2/desktop.png),
[mobile screenshot](assets/chat-seed-team-dev2/mobile.png),
[mobile People panel](assets/chat-seed-team-dev2/mobile-people.png).
Repeatable live check: `TEAM_CHAT_STATE=<protected accounts.json> node e2e/team-chat-live.mjs`.

## Automated verification

Focused seed, real migrated HTTP router integration, provisioning collision,
CLI capability, avatar catalog and authentication retry tests passed. The seed
race subset passed. `go vet ./...`, ESLint (zero errors, 32 existing warnings),
the four executable agent invariants, shell syntax and diff whitespace passed.
OpenAPI remains 614 operations; CLI inventory now contains 859 commands.

Full `go test ./... -count=1 -timeout=30m` completed: 132 packages passed,
including API, database, CLI and backup. Its single failure was the existing
raw-file-write invariant detecting the new empty O_EXCL invocation lock. Added
an exact source-line exception with a written reason: this sentinel contains no
data, and replacing it through a durable rename would defeat mutual exclusion.
An independent review accepted that narrow change; no production code changed.
The complete affected `internal/consolidate` package then passed on rerun
(14.315s), giving passing results for all 133 tested packages. Repeated Go vet
also passed. The original full invocation exited 1; it is not reported as an
uninterrupted green run. Public `/api/health` returned `status: ok`.
