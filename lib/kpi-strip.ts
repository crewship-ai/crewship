/**
 * Whether a KPI strip has nothing to report (Harbor): every value it can show
 * is zero. `null` marks a tile this reader cannot see — it neither keeps nor
 * collapses the strip, but a strip made only of those is not "empty", it is
 * unknown.
 */
export function kpiStripIsEmpty(values: ReadonlyArray<number | null>): boolean {
  const known = values.filter((v): v is number => v !== null)
  return known.length > 0 && known.every((v) => v === 0)
}
