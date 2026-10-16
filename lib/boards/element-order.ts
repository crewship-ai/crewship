import type { BoardElement } from './revision-adapter'

const indexOf = (element: BoardElement) => typeof element.index === 'string' && /^[A-Za-z0-9]{1,128}$/.test(element.index) ? element.index : null
const compare = (a: string, b: string) => a < b ? -1 : a > b ? 1 : 0

/** Reconstruct drawing z-order from saved fractional indexes before handing an
 * array to Excalidraw. Its restore path treats array order as authoritative and
 * repairs out-of-order indexes, which would create a false unsaved edit.
 * Missing/invalid indexes sort after indexed drawings; IDs break ties without
 * rewriting any saved fields or granting revision authority. */
export function orderBoardElements<T extends BoardElement>(elements: readonly T[]): T[] {
  return [...elements].sort((a, b) => {
    const left = indexOf(a), right = indexOf(b)
    if (left !== null && right !== null) return compare(left, right) || compare(a.id, b.id)
    if (left !== null) return -1
    if (right !== null) return 1
    return compare(a.id, b.id)
  })
}
