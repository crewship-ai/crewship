import { useEffect, useRef } from "react"

/**
 * Folds something (the sessions list) while `wanted` holds, and puts it back as the person had it once it no longer
 * does. Opening it meanwhile is the person's choice: it is not folded again until `wanted` starts over.
 */
export function useFoldWhile(wanted: boolean, collapsed: boolean, setCollapsed: (value: boolean) => void): void {
  const latest = useRef(collapsed)
  useEffect(() => { latest.current = collapsed }, [collapsed])
  const before = useRef<boolean | null>(null)
  useEffect(() => {
    if (wanted && before.current === null) { before.current = latest.current; setCollapsed(true) }
    else if (!wanted && before.current !== null) { setCollapsed(before.current); before.current = null }
  }, [wanted, setCollapsed])
}
