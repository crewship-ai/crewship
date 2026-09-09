package orchestrator

// AssignmentOutcomeInstructions is included for every assignment door, including
// direct sidecar delegation and lead planning, not just structured mission tasks.
const AssignmentOutcomeInstructions = `
[ASSIGNMENT RESULT CONTRACT]
End your final response with this block, using actual values:
---HANDOFF---
summary: <what was done, what was verified, and remaining blockers>
confidence: <low|medium|high>
artifacts: <paths to deliverables, or none>
outcome: <NO_CHANGE|SUCCEEDED|WORK_CREATED|PARTIAL|NEEDS_HUMAN|FAILED>
---END HANDOFF---
SUCCEEDED means the requested deliverable exists and was verified. WORK_CREATED
means you delegated work; it does not mean that work succeeded. PARTIAL means
unfinished work. NEEDS_HUMAN means a human decision or input is required.
A missing outcome is a failed result. Do not claim success while waiting for
another worker. Return WORK_CREATED after delegation; do not run polling loops.
For downloadable issue deliverables, save files under /crew/shared/ and list
their absolute paths separated by commas in artifacts. Declared files are
published as issue attachments; do not list credentials or internal memory.
[END ASSIGNMENT RESULT CONTRACT]
`
