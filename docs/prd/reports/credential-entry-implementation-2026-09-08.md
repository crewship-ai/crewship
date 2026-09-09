# Credential entry UX — dev3, 2026-09-08

Implements the guided entry proposal in `../credential-entry-ux-proposal-2026-09-08.md`, preserving Crewship accents, brand marks and CreateSurface.

## Entry and lifecycle

- Six secret tiles, no default selection, direct transition into Details; the icon picker belongs beside Name.
- Compact selected type/provider with Change. Metadata stays in memory across Back. Changing incompatible type/provider/method asks before clearing value and extra fields; choosing the same option preserves input.
- Name and required parts gate Details, with focus on the missing field. Description/account label/tags, optional type-specific parts and security settings have named disclosures. Key-pair ID and secret sit together on desktop.
- Provider search covers all 12 registered providers. OpenAI presents code/import/API key in one method group; Anthropic setup token/API key; Google import/API key; remaining providers only their supported API key.
- Local UTF-8 text upload for multiline primary values and secret parts; filename, size, replace/remove controls. Empty, binary, malformed JSON and files over the API's 64 KiB value limit are rejected without echoing contents. Uploaded CRLF and secret whitespace survive serialization; optional empty fields are omitted. Local shape checks are not connection verification.
- Secret edit no longer trims replacement bytes. Existing type is preserved by the edit form.
- The final Use step defaults to Save for later. Explicit Assign now offers agents (avatar/name), crews, or all agents to OWNER/ADMIN. Provider bindings use the supported provider slot rather than exposing an arbitrary env input.
- Credentials are created with WORKSPACE visibility and without crew links. Assignment is a separate binding write. This matters because `credential_crews` delivers to crew agents; scope alone does not. See `internal/api/credential_delivery.go` and `credential_bindings.go`.
- Occupied same-scope slots are checked before create and still protected by the backend unique constraint. No binding is overwritten.
- Partial saves keep the created ID and successful field/binding writes. Retry only attempts incomplete writes. The user can instead open the saved credential. An uncertain create response requires checking the list instead of blindly repeating POST.
- Workspace changes remount the wizard; a directly reused wizard also refuses to submit its original draft into a different workspace. Close unmounts secret state.
- Successful saves open the saved detail. Saving, connection checks and runtime access remain distinct. No automatic upstream probe or rotation is implied.

## Re-login

The old route reopened Create and could produce a duplicate account. Re-login now carries the original ID and PATCHes only the login value/mode. For PROVIDER_LOGIN, the handler splits the imported login and replaces its access token, standard login fields and refresh state in the same transaction/audit operation. Metadata and bindings remain intact. Invalid imports leave the existing login untouched. Existing stricter sensitivity is retained.

Device-code creation still creates an account before the last wizard step. The UI explicitly says it already exists and offers Finish setup/Close setup. Existing-account re-login uses import/setup-token/API key: this backend has no device-code flow targeting an existing ID, so that option is not advertised for re-login. Legacy login types retain their existing PATCH value contract.

## Deliberate boundaries

No arbitrary filesystem target, certificate issuance/fingerprint/key-pair proof, universal login test or upstream password rotation is introduced. Binding success configures delivery; it does not override Keeper/workspace policy or select the agent's model. A lost create response is not resolved by inventing idempotency. Existing legacy credential types are not converted by the new wizard.

## Verification

- 615 frontend tests in 27 files passed (credentials page, components, credential utilities and brand registry).
- Full lint: zero errors, 32 existing warnings.
- Static production export passed.
- Browser tests cover 1280/390 px, role restrictions, provider methods, edit, Login entry; file upload and explicit agent assignment additionally verify exact CRLF payloads and separate create/field/binding requests. Fixtures only, no actual secrets/provider accounts.
- New Go regression covers atomic provider re-login, preserved binding/identity and malformed-import rejection. Full Go suite and vet results are recorded below after completion.

Deployment: rebuilt/staged static export, preserved `web/out/.placeholder.html`, reloaded only `crewship-ws@3`. API health returned `{"status":"ok"}`. All 13 Playwright acceptance scenarios passed against `https://crewship-dev3.unifylab.cz` (22.1s). Full API package passed in 632.803s; `go vet -p 2 ./...` passed. Final frontend follow-up checks: 67 targeted tests passed after metadata/preflight refinements; production build and targeted ESLint passed again.

Final Go outcome: the full run completed; API passed (632.803s), database passed (712.197s), and every other package passed except `web`, whose compilation retained filenames from the export that was replaced during this long run. Reran `go test -p 2 ./web -count=1` against the stable final export: passed (0.003s). No code/test assertion failures remained. Logs: `/tmp/credential-entry-go.log`, `/tmp/credential-entry-web-retry.log`, `/tmp/credential-entry-vet.log`. Avoid staging/replacing `web/out` during future full Go runs; Go loads embed filenames before deferred package compilation.
