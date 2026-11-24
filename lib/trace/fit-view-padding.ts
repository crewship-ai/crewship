// React Flow 12 added a fraction of the fitted content size as padding.
// React Flow 13 accepts a percentage of the viewport on each side.
export function legacyFitViewPadding(padding: number): `${number}%` {
  return `${(50 * padding) / (1 + padding)}%`
}

export const controlsFitViewOptions = { padding: legacyFitViewPadding(0.1) }
