// Parity with RunStore.MarkTerminal is checked in routine-run-status.test.ts.
export const terminalRoutineRunStatuses = [
  "completed",
  "failed",
  "cancelled",
  "interrupted",
  "dry_run",
] as const

export function isTerminalRoutineRun(status: string): boolean {
  return terminalRoutineRunStatuses.some((value) => value === status.toLowerCase())
}
