# Harbor Goods business demos

One fictional small business, four independent projects. All customer names,
invoices and shipments are sample data. No email, payment, claim or public
website request leaves the demo. The optional AI step uses the configured AI
provider. The live monitor posts only to the current Crewship installation.

| Project | Check | Human decision / result |
| --- | --- | --- |
| Sales | Find the inquiry waiting 26 hours | Approve a reply into the local outbox |
| Finance | Match invoices to two bank payments | Approve a reminder; the debt remains unpaid |
| Marketing | Read local delivery records over loopback HTTP | Record the repaired sample delivery |
| Shipping | Find a late shipment within its claim window | Approve a claim into the local outbox |

Each story has a TODO Issue, a Page folder and three routines: check, optional
AI draft, and resolve. The first check comments on the Issue and notifies Inbox.
Resolve waits for a human for Sales, Finance and Shipping. `Keep open` saves no
artifact. Approval saves a local receipt and closes the prepared Issue. Marketing
is a deterministic local simulation, not a demonstration of a real email provider.

The catalogue is `catalogue.json`; fixtures and Page storytelling use the same
records. `story.py` is the executable check and SQLite/outbox store. `connector.py`
is a small working MCP integration exposing the records to agents. The seed
writes `bindings.json` with real Issue identifiers after creating the Issues.
No fixture contains a database ID, API token or Codex login.

## Run and verify

```sh
crewship seed --wait-provision
crewship seed verify --timeout 2m
# Disposable demo workspace: changes Issues to DONE and approves local actions.
crewship seed verify --complete-demo --timeout 2m
```

The default check needs Python and a provisioned crew, but no model credentials.
`Draft with AI` needs a working provider login/API key. A prepared template is
labelled as sample content; it is never presented as AI-generated output.
For Codex set `SEED_CODEX_AUTH_FILE` to a private local auth.json file before
seeding. Only `.env.example` contains configuration examples; never version the
login file or its contents.

Use `seed --nuke --yes --wait-provision` only on a disposable workspace to return
to the initial TODO state. An ordinary re-seed preserves outcomes and existing
Issues. Each project's active actions share a concurrency key. Stable Page action keys
coalesce repeated clicks across reloads while an approval is pending. Direct API
callers must also supply an idempotency key; parked approvals release the runtime
concurrency slot. One SQLite key and outbox file per story prevent
duplicate local delivery artifacts. A failed routine is not a completed story:
inspect its History and Issue before retrying.

## Live container example

`live.py` samples its own cgroup memory every five seconds, independently of
viewers, for at most 15 minutes. Start/Stop are short control routines; sample
publication does not run through routines or an LLM. File locking allows one
worker. A Page webhook can write only the monitor's memory panel.

The seed creates a one-panel webhook and delivers `live-private.json`. The
collector posts to the loopback sidecar, which forwards the capability over the
existing Crewship connection. The public webhook handler still enforces scope,
revocation, schema and rate limits. No public DNS, callback URL or private-network
permission is needed. Never copy the private callback file into source control.
Stop takes up to one network timeout plus the five-second sample interval.

## Extend

1. Add a story with sample rows, crew and agent to `catalogue.json`.
2. Add its deterministic rule to `story.py` and a fixture test including no-finding
   and repeat-completion cases. Unknown story types must fail, not reuse a rule.
3. Add a matching TODO Issue/project in `builtin/issues.yaml` and Page panel
   definitions in `builtin/pages.yaml`. The generic Page and three routines are
   generated from story metadata. Use a dedicated agent to avoid contention.
4. Keep producers distinct: check writes records/finding; draft writes draft;
   resolve writes proposal/outcome/resolved-records. Action controls use status.v1.
5. Run the Python tests, Go seed tests, then the live verifier and a browser check.

Do not put a live credential, third-party polling loop, auto-start schedule or
fake completed run in a fixture. Add optional service-specific demos separately.
