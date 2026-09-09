# Credential create/edit clarity — 2026-09-08

User requested a coordinated improvement to create and edit UX. This is a
frontend-only increment; authorization, encryption, provider protocols and
API payload semantics are unchanged.

- Edit separates metadata visually, removes the duplicate brand inside the
  name input, and shows the binding rename warning only when the name changes.
- Replacement remains explicit and opt-in, with a short unchanged-value status
  and an issuer-side warning when replacement is enabled. Unchecking still
  clears the draft and hides the input; stored values never prefill it.
- A shared metadata-only summary shows scope, Keeper tier and configured expiry
  above the access disclosure. Missing optional summary values are omitted;
  this does not assert successful authentication or effective agent access.
- The tier trigger renders only the selected label, keeping detailed tier
  explanations in the options and helper text instead of clipping them inside
  a single-line control.
- The create wizard's final step summarizes current scope/protection and states
  that assignments and Keeper still govern actual access. Provider-specific
  connection steps and all validation are retained.
- Spacing, disclosure touch targets and tag help are simplified. The shared
  Routines-style shell retains its separate scrolling body and fixed footer.

Verification: 405 credential component tests passed, including three new summary
tests; targeted ESLint clean; production static build passed. Nine Playwright
scenarios passed against dev3's local frontend, using intercepted API fixtures
only, at 1280px and 390px: provider RBAC, onboarding, metadata-only edits, typed
replacement, extra fields, tag behavior and visible save action. Screenshots
were inspected. No real credential was saved/replaced for these checks.

Full repository lint passed with zero errors and 32 existing warnings.
`go test -p 2 ./... -count=1 -timeout 40m` passed (API 104.592s,
database 616.955s), followed by `go vet -p 2 ./...` passing. This verifies this
increment, not the entire release PRD or the remaining credential security work.
Logs: `/tmp/credential-ux-all-tests.log`, `/tmp/credential-ux-eslint.log`,
`/tmp/credential-ux-build.log`, `/tmp/credential-ux-browser.log`,
`/tmp/credential-ui-full-lint.log`, `/tmp/credential-ui-go.log`,
`/tmp/credential-ui-vet.log`.

No commit, PR or service reload has been performed for this increment. The
running dev3 development frontend reads these local source changes.
