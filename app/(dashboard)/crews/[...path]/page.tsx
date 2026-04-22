"use client"

import { useEffect } from "react"
import { useParams, useRouter } from "next/navigation"

export const dynamicParams = true
export function generateStaticParams() {
  return [{ path: ["_"] }]
}

export default function LegacyCrewsCatchAll() {
  const router = useRouter()
  const params = useParams()
  const path = Array.isArray(params.path) ? params.path.join("/") : ""
  const target = `/fleet/crews${path ? `/${path}` : ""}`

  useEffect(() => {
    router.replace(target)
  }, [router, target])

  return (
    <>
      <meta httpEquiv="refresh" content={`0;url=${target}`} />
      <noscript>
        <p>Redirecting to <a href={target}>{target}</a>…</p>
      </noscript>
    </>
  )
}
