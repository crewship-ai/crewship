# Activity / Work consolidation — 21 September 2026

Issue #2636. Base: main `8a1ca5fc3`. The September 15 webhook handoff explicitly
left the proposed Work-to-Activity merge unfinished. This change implements
that navigation follow-up without changing the work/delivery API contracts.

Activity has Overview, Work and Deliveries views. Only the chosen surface
mounts, so the work ledger does not also start the overview's event stream.
The URL records the view and preserves existing run/issue deep-link parameters.
The old `/work` route replaces itself with `/activity?section=work`; an explicit
deliveries section is preserved. The shared desktop/mobile primary navigation
no longer contains Work. Workspace changes remount the workspace surface.

## Evidence

- 85 targeted tests passed, including rendering the actual ledger inside the
  new shell, delivery-to-work navigation, workspace filter reset and old URLs.
- Full frontend: 773 files / 9,218 tests passed.
- Production export passed; ESLint: 0 errors / 30 existing warnings.
- Chromium against the new production export through a temporary local HTTP
  proxy to DEV1's real API: real credentials login, legacy redirect, absence
  of the primary Work link, view switching, reload, Back/Forward, keyboard
  tab navigation and 390px page overflow check passed. No page exceptions.
  The ledger was empty in this test account. Populated delivery-to-work
  interaction is covered by the component test, not claimed as live evidence.
- Screenshots and browser report are retained at
  `/srv/crewship/backups/crewship_1/activity-work-20260921/`.

The browser exercise used a temporary preview, not a deployment to the public
DEV1 frontend. It does not validate live websocket reconnection or the PRD's
human usability gate. Full Go verification and review are tracked in the PR.

## Remaining Routines acceptance work

This change does not close the Routines PRD. The follow-up found that R8's
builder has no production caller after the editor replacement; #2637 restores
it in the active editor. Performance comparisons, current authenticated
acceptance evidence and the five-user usability gate remain separately tracked.
