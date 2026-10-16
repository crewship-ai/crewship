import * as React from "react"

import { LAYOUT_BREAKPOINTS } from "@/hooks/use-layout-breakpoint"

/** The phone edge of the layout scale (Tailwind `md`). */
const MOBILE_BREAKPOINT = LAYOUT_BREAKPOINTS.tablet

/** Returns true when the viewport is narrower than 768px (mobile breakpoint). */
export function useIsMobile() {
  const [isMobile, setIsMobile] = React.useState<boolean | undefined>(undefined)

  React.useEffect(() => {
    const mql = window.matchMedia(`(max-width: ${MOBILE_BREAKPOINT - 1}px)`)
    const onChange = () => {
      setIsMobile(window.innerWidth < MOBILE_BREAKPOINT)
    }
    mql.addEventListener("change", onChange)
    setIsMobile(window.innerWidth < MOBILE_BREAKPOINT)
    return () => mql.removeEventListener("change", onChange)
  }, [])

  return !!isMobile
}
