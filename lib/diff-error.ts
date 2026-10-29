/** Explicit preview refusals; never treat them as an empty successful diff. */
export function diffErrorMessage(status: number, fallback: string): string {
  if (status === 413) return "The diff is too large. View each version separately or ask an administrator to adjust the diff limits."
  if (status === 429) return "Comparisons are busy. Try again shortly."
  return fallback
}
