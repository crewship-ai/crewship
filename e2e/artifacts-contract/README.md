# Artifacts acceptance contract foundation

This opt-in runner records seven ordered steps for each artifact type. It is
not registered in global Playwright or CI suites. Run harness tests with:

```sh
crewship-heavy node --test e2e/artifacts-contract/contract.test.mjs
```

These tests exercise the harness with injected adapters. They invoke no
provider, application, real CLI binary or browser and are not live acceptance.

| Step | Document adapter | Acceptance scope |
| --- | --- | --- |
| agentCreate | Actual CLI agent run, durable result/read, ADMIN 404 and OWNER read | T-AG-01 / T-ACL-02 subchecks |
| openChatPanel | Playwright clicks saved result in the existing chat; waits for editor | T-AG-01 subcheck |
| iterateMessage | Incomplete | Existing-document agent iteration needs its own adapter |
| humanEditVersions | Browser save/reload and CLI revision advancement; incomplete | T-DOC-01 subchecks; T-DOC-03 history/compare/restore absent |
| explicitSharing | Incomplete | Sharing and revocation need positive/negative identity checks |
| gallery | Incomplete | Catalog discovery must verify accessible and hidden objects |
| export | Incomplete | Download, exact confirmed revision and access checks absent |

Source surfaces are `cmd/crewship/cmd_run.go`, `cmd_doc_results.go`,
`cmd_doc.go`, `components/features/chat/document-result-cards.tsx`,
`components/features/pages/page-document.tsx`, and
`internal/api/documents_chat_results.go`. Requirement IDs identify partial
coverage, not complete requirement passes. The open card-to-editor behavior is
asserted as a product dependency; the harness does not patch or mock it.

## Two evidence variants

`deterministic-fixture` is the CI template. The CLI entrypoint currently records
**incomplete** because a deterministic provider fixture is not wired. It does
not launch a provider or browser. A future fixture must drive actual agent
execution and actual CLI subprocesses; a test-only operation returning success
cannot close missing production behavior.

`manual-sonnet-5.5` invokes the configured real agent and may spend provider
credits. Use only an authorized dedicated test instance with that agent already
configured for Sonnet 5.5. The model designation is operator input, not verified
provider provenance. Inspect durable run/provider records independently before
claiming any live acceptance. `liveAcceptance` is deliberately always false in
these foundation reports; synthetic reports can never become live evidence.

No full seven-step Document pass is possible with this initial adapter. Other
types return seven incomplete rows. Failed operations stop dependent work and
retain all seven rows; unsupported and unexecuted steps never become passed or
skipped. Exit codes: 0 all steps passed, 2 incomplete, 1 failure/setup error.

## Manual invocation and private configuration

Install the repository's locked Playwright dependency and Chromium through the
normal development setup. Supply an existing release binary with a built UI,
a dedicated server with the Documents engine enabled, and three separate human
accounts with live OWNER, ADMIN and MEMBER memberships. MEMBER must already
have an empty private chat with the configured agent. Do not reuse a chat or
title from a previous run. Browser sessions are checked against the configured
user IDs, and the live membership roster and CLI whoami role/email are checked before mutations.

Create a mode-0600 JSON file outside the checkout:

```json
{
  "type": "document",
  "variant": "manual-sonnet-5.5",
  "providerModel": "Sonnet 5.5",
  "dedicatedTarget": true,
  "server": "http://127.0.0.1:3900",
  "binary": "/absolute/path/to/crewship",
  "workspace": "workspace-id",
  "agent": "document-author",
  "chatId": "fresh-member-chat-id",
  "chatPath": "/agents/document-author?chat=fresh-member-chat-id",
  "title": "Unique contract document",
  "initialText": "Initial contract text",
  "humanText": "Human replacement text",
  "identities": {
    "OWNER": {"userId":"owner-id","cliConfig":"/private/owner-cli.json","storageState":"/private/owner-browser.json"},
    "ADMIN": {"userId":"admin-id","cliConfig":"/private/admin-cli.json","storageState":"/private/admin-browser.json"},
    "MEMBER": {"userId":"member-id","cliConfig":"/private/member-cli.json","storageState":"/private/member-browser.json"}
  }
}
```

`chatPath` is the real URL that opens the selected existing chat on your target;
verify it manually rather than assuming the illustrative query above matches
your deployment. Each CLI config must authenticate as the named account on the
same server; no login, profile switch or credential provisioning occurs. CLI
environment overrides are removed and the target workspace/server are explicit.
The OWNER read intentionally exercises the product's audited owner-access path.

```sh
crewship-heavy node e2e/artifacts-contract/run.mjs /private/contract.json
```

Reports go into a fresh mode-0700 temporary directory printed at startup; the
JSON file is mode 0600. Raw command output, cookies and document content are
not reported. A failed assertion intentionally reports a generic operation
failure to avoid leaking private provider responses. Setup failure is failure,
not acceptance. Keep private input and evidence files out of commits/issues.

The runner creates a document and appends a message to the supplied chat, then
replaces that document's text. It closes only its own browser contexts. It does
not delete durable resources or cancel server-side runs after CLI timeout; use
a disposable environment and investigate timeouts before cleanup. Run isolated
servers only through `scripts/throwaway-server.sh`; never point this at a shared
workspace or production. Reports and fixture designation do not certify cleanup,
provider identity or a full privacy/audit contract.
