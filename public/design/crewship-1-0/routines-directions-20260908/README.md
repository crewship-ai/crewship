# Routines design review — standalone prototypes

Open `index.html` for five directions, three interactive layouts and the full
Czech analysis. Each A/B/C HTML is standalone, including its CSS and JavaScript.
The product labels are English; design-review notes are Czech.

- `a-recipe.html`: readable recipe sheet.
- `b-flow.html`: one workflow map across recipe, editing and execution.
- `c-workbench.html`: step navigation, editor and contextual evidence.
- `#edit` and `#run`: direct entry states when opening a prototype.
- `analysis.md`: capabilities, observed findings, proposals and acceptance gates.

All data is illustrative. Drafts, version changes, run states, AI authoring,
review and calendar entries are local memory only and reset on reload. The
Content Security Policy blocks network connections. No real API, model,
credential, routine, schedule or approval is invoked. There is no analytics.

The controls demonstrate interactions rather than exporting runnable DSL.
Result/source editors and external workspace links show contextual previews.
Calendar tiles demonstrate the existing calendar's place in the proposed UX,
with September 2026 as the example month. The actual engine's limits, including
unwired inline Python/Go/Bash and foreach waits, are explained in the analysis.

The separate state selector belongs to the prototype review toolbar; it is not
a proposed client control for changing real execution outcomes. Run data in a
production implementation must always come from recorded backend evidence.

Verification evidence on the dev1 host:
`/tmp/routines-directions-test.log`, `/tmp/routines-layout-continuity.log`.

Updated recommendation: `client-workspace.html` adds an outcome-first client
workspace, state-specific emphasis, compact direct editing, preflight repair,
version impact, recovery explanations and an explicitly subjective priority
matrix (`#priorities`). This supersedes the initial recommendation of A.
The sample recipe, checks, connection availability and all runtime evidence
are illustrative. See analysis section 16 for scope and production contracts.
