# Public documentation and private working context

The public repository must contain everything a contributor needs to build,
test, understand and operate Crewship. Current contracts, accepted architectural
decisions, contributor instructions, public examples and regression tests stay
here. A private checkout must never be a build, test or CI dependency.

Maintainer research, business plans, session handoffs, instance-specific audit
evidence and experimental scripts live in a separate private repository. New
internal working records go there, not into `docs/prd/`, `public/` or an
assistant-specific directory. Public proposals intended for community discussion
can still be submitted here with an explicit status and a public review context.

## Optional discovery for maintainers

Keep the private repository outside this checkout. Configure its absolute path
locally (this setting is not committed):

```sh
git config --local crewship.internalContext /absolute/path/to/private-context
git config --get crewship.internalContext
```

When configured and accessible, agents read that repository's `AGENTS.md` and
topic index, then only the records relevant to their task. When absent, work
from public documentation; do not require private access from contributors or
put private credentials in configuration. Use separate worktrees when several
agents edit the private repository concurrently.

## Daily authoring workflow

| What you are writing | Where to write it | How it is saved |
| --- | --- | --- |
| Public implementation contract, guide or reproducible test | The owning public category in the [documentation map](../README.md) | Normal product branch, commit and PR |
| Internal research, plan or session handoff | The private repository's topic area | Follow that repository's instructions |
| Internal record on a workstation with a configured `internal-docs/` alias | `internal-docs/<topic>/<dated-name>.md` | Follow the private repository's automation/status instructions; Git ignore alone does not save it |
| Generated inventory, build output or test report | Its ignored output location | Regenerate it; do not version it as authored documentation |

Before writing internal context, resolve `crewship.internalContext` and read
that repository's `AGENTS.md` and topic index. If the checkout or alias is
missing, configure private access first; do not fall back to a public PRD or
assistant directory. Synchronization is workstation configuration, not a
capability supplied by cloning the public repository.

Keep a record under its topic, link it from that topic's index, and state its
purpose, date, status and product revision. Link public implementation evidence
from private notes. Public documents must remain understandable without access
to those notes: publish the necessary contract and rationale when a decision
becomes product behaviour. A dated handoff should name its next action and
unresolved questions; a later document should point back to what it supersedes.

## Moving existing records

1. Classify the file by its consumers, not its filename or age. A document
   called PRD can be the specification for a shipped feature.
2. Copy it to the private repository with original path, product revision,
   SHA-256 and original authorship/license information. Preserve evidence bytes.
3. Scan for credentials, push to the private remote, and verify hashes in a
   fresh clone before removing anything from the public working tree.
4. Preserve required public contracts and tests; update live references. Links
   to previously published historical evidence may use an immutable public
   commit URL. Never rewrite shipped SQL migrations to tidy a comment.
5. Run the documentation gates and the checks relevant to affected tools.

The verified migration batches remove internal session/instance reports,
research and review records, assistant configuration, visual prototypes and
retired experiment scripts. Public implementation contracts, reproducible tests
and required design context remain available. Remaining design documents are
indexed by their public purpose; their dated status is not proof that every
proposal is implemented. Working records previously published remain in public
Git history. Moving current files does not make old versions secret, revoke
their licenses or transfer copyright.

Generated documentation inventory reports are ignored local build output in
`docs/prd/reports/`; their generator and CI checks remain public. They do not
belong in either repository's version history.

## Publication boundary

Root assistant instructions (`CLAUDE.md`, `CODEX.md`, `GEMINI.md`), workspaces
(`.claude/`, `.codex/`, `.cursor/`), `.github/copilot-instructions.md`, local
`internal-docs` aliases, internal mockups and captured run reports are ignored.
Shared public contributor instructions belong in `AGENTS.md`; workstation
instructions belong in optional private context. The
`agents-invariants` CI check inspects tracked paths, so a forced add cannot
silently reintroduce these categories. The reports directory retains only its
README and a documented public regression source fixture. This path guard
cannot classify arbitrary prose; review the audience of new documentation.

Workstation-specific synchronization, private remotes and access keys are
configured outside this public checkout. Public builds, tests and CI do not
require that setup. A directory ignore rule alone does not publish or back up
its contents. Shipped migration comments are immutable; historical document
paths in those comments refer to the source revision, not a private runtime
dependency.
